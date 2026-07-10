package engine

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

type OrderRequest struct {
	UserID     uint      `json:"user_id"`
	Symbol     string    `json:"symbol"`
	IsBuyOrder bool      `json:"is_buy_order"`
	Price      float64   `json:"price"`
	Quantity   float64   `json:"quantity"`
	BotID      string    `json:"bot_id"`
	Expiration time.Time `json:"expiration"`
}

type AddOrderResult struct {
	Order   *Order  `json:"order"`
	Matches []Match `json:"matches"`
}

type CancelOrderResult struct {
	Matches []Match `json:"matches"`
}

type Match struct {
	BuyID    uint    `json:"buy_id"`
	SellID   uint    `json:"sell_id"`
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
}

type Market struct {
	Symbol     string
	BuyHeap    *OrderHeap
	SellHeap   *OrderHeap
	ExpiryHeap *OrderHeap
	LastPrice  float64
	BuyVolume  float64
	SellVolume float64
}

type OrderMatchingSystem struct {
	markets map[string]*Market
	mu      sync.Mutex
	db      *gorm.DB
	logger  *zap.Logger
}

func NewOrderMatchingSystem(db *gorm.DB, logger *zap.Logger) *OrderMatchingSystem {
	return &OrderMatchingSystem{
		markets: make(map[string]*Market),
		db:      db,
		logger:  logger,
	}
}

func (oms *OrderMatchingSystem) LoadFromDB() error {
	oms.mu.Lock()
	defer oms.mu.Unlock()

	var orders []Order
	if err := oms.db.Where("status IN ?", []string{string(StatusCreated), string(StatusPartial)}).Find(&orders).Error; err != nil {
		return fmt.Errorf("load orders: %w", err)
	}

	oms.logger.Info("loading orders from database", zap.Int("count", len(orders)))

	for i := range orders {
		o := &orders[i]
		market := oms.getOrCreateMarket(o.Symbol)

		if !o.Expiration.IsZero() {
			heap.Push(market.ExpiryHeap, o)
		}
		if o.IsBuyOrder {
			heap.Push(market.BuyHeap, o)
		} else {
			heap.Push(market.SellHeap, o)
		}
	}

	for _, market := range oms.markets {
		oms.matchLocked(oms.db, market)
	}

	oms.logger.Info("orders loaded and matched",
		zap.Int("markets", len(oms.markets)),
	)
	return nil
}

func (oms *OrderMatchingSystem) getOrCreateMarket(symbol string) *Market {
	market, exists := oms.markets[symbol]
	if !exists {
		market = &Market{
			Symbol:     symbol,
			BuyHeap:    NewBuyHeap(),
			SellHeap:   NewSellHeap(),
			ExpiryHeap: NewExpiryHeap(),
		}
		oms.markets[symbol] = market
	}
	return market
}

func (oms *OrderMatchingSystem) AddOrder(req *OrderRequest) (*AddOrderResult, error) {
	if req.Quantity <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}
	if req.Price <= 0 {
		return nil, fmt.Errorf("price must be positive")
	}
	if req.Symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}

	order := &Order{
		UserID:     req.UserID,
		Symbol:     req.Symbol,
		IsBuyOrder: req.IsBuyOrder,
		Price:      req.Price,
		Quantity:   req.Quantity,
		Status:     StatusCreated,
		BotID:      req.BotID,
		Expiration: req.Expiration,
	}

	oms.mu.Lock()
	defer oms.mu.Unlock()

	market := oms.getOrCreateMarket(order.Symbol)

	if err := oms.db.Create(order).Error; err != nil {
		return nil, fmt.Errorf("failed to create order: %w", err)
	}

	oms.logger.Info("order created",
		zap.Uint("id", order.ID),
		zap.String("symbol", order.Symbol),
		zap.Bool("buy", order.IsBuyOrder),
		zap.Float64("price", order.Price),
		zap.Float64("qty", order.Quantity),
	)

	if !order.Expiration.IsZero() {
		heap.Push(market.ExpiryHeap, order)
	}
	if order.IsBuyOrder {
		heap.Push(market.BuyHeap, order)
	} else {
		heap.Push(market.SellHeap, order)
	}

	matches := oms.matchLocked(oms.db, market)

	return &AddOrderResult{Order: order, Matches: matches}, nil
}

func (oms *OrderMatchingSystem) matchLocked(tx *gorm.DB, market *Market) []Match {
	var matches []Match
	batch := make(map[uint]*Order)

	for market.BuyHeap.Len() > 0 && market.SellHeap.Len() > 0 {
		buy := market.BuyHeap.Peek()
		sell := market.SellHeap.Peek()

		if buy == nil || sell == nil || buy.Price < sell.Price {
			break
		}

		matchQty := min(buy.Remaining(), sell.Remaining())

		// use the resting (older) order's price
		var matchPrice float64
		if buy.CreatedAt.Before(sell.CreatedAt) {
			matchPrice = buy.Price
		} else {
			matchPrice = sell.Price
		}

		buy.FilledQty += matchQty
		sell.FilledQty += matchQty

		market.LastPrice = matchPrice
		market.BuyVolume += matchQty
		market.SellVolume += matchQty

		matches = append(matches, Match{
			BuyID:    buy.ID,
			SellID:   sell.ID,
			Price:    matchPrice,
			Quantity: matchQty,
		})

		oms.logger.Debug("order matched",
			zap.Float64("price", matchPrice),
			zap.Float64("qty", matchQty),
			zap.Uint("buy_id", buy.ID),
			zap.Uint("sell_id", sell.ID),
		)

		if buy.Remaining() <= 0 {
			buy.Status = StatusFilled
			heap.Pop(market.BuyHeap)
			removeFromHeap(market.ExpiryHeap, buy.ID)
		} else {
			buy.Status = StatusPartial
		}
		batch[buy.ID] = buy

		if sell.Remaining() <= 0 {
			sell.Status = StatusFilled
			heap.Pop(market.SellHeap)
			removeFromHeap(market.ExpiryHeap, sell.ID)
		} else {
			sell.Status = StatusPartial
		}
		batch[sell.ID] = sell
	}

	for _, o := range batch {
		tx.Model(o).Updates(map[string]interface{}{
			"status": o.Status, "filled_qty": o.FilledQty,
		})
	}

	return matches
}

func (oms *OrderMatchingSystem) CancelOrder(orderID uint) (*CancelOrderResult, error) {
	oms.mu.Lock()
	defer oms.mu.Unlock()
	return oms.cancelOrderLocked(orderID)
}

func (oms *OrderMatchingSystem) cancelOrderLocked(orderID uint) (*CancelOrderResult, error) {
	var order Order
	if err := oms.db.First(&order, orderID).Error; err != nil {
		return nil, fmt.Errorf("order not found: %w", err)
	}

	if !order.IsActive() {
		return nil, fmt.Errorf("cannot cancel order with status %s", order.Status)
	}

	oms.db.Model(&order).Update("status", StatusCanceled)

	market := oms.markets[order.Symbol]
	if market == nil {
		return &CancelOrderResult{}, nil
	}

	if order.IsBuyOrder {
		removeFromHeap(market.BuyHeap, orderID)
	} else {
		removeFromHeap(market.SellHeap, orderID)
	}
	if !order.Expiration.IsZero() {
		removeFromHeap(market.ExpiryHeap, orderID)
	}

	matches := oms.matchLocked(oms.db, market)

	oms.logger.Info("order canceled", zap.Uint("id", orderID))
	return &CancelOrderResult{Matches: matches}, nil
}

func (oms *OrderMatchingSystem) GetOrder(orderID uint) (*Order, error) {
	var order Order
	if err := oms.db.First(&order, orderID).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func (oms *OrderMatchingSystem) GetOrderBook(symbol string) (bids, asks []*Order) {
	oms.mu.Lock()
	defer oms.mu.Unlock()

	market := oms.markets[symbol]
	if market == nil {
		return
	}

	for _, o := range market.BuyHeap.orders {
		if o.IsActive() {
			bids = append(bids, o)
		}
	}
	for _, o := range market.SellHeap.orders {
		if o.IsActive() {
			asks = append(asks, o)
		}
	}
	return
}

func (oms *OrderMatchingSystem) GetLastPrice(symbol string) float64 {
	oms.mu.Lock()
	defer oms.mu.Unlock()

	market := oms.markets[symbol]
	if market == nil {
		return 0
	}
	return market.LastPrice
}

func (oms *OrderMatchingSystem) StartExpirationLoop(ctx context.Context, symbol string) {
	// Initialize market under lock to avoid race
	oms.mu.Lock()
	oms.getOrCreateMarket(symbol)
	oms.mu.Unlock()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			oms.logger.Info("expiration loop stopped", zap.String("symbol", symbol))
			return
		case <-ticker.C:
			oms.processExpirations(symbol)
		}
	}
}

func (oms *OrderMatchingSystem) processExpirations(symbol string) {
	oms.mu.Lock()
	defer oms.mu.Unlock()

	market := oms.markets[symbol]
	if market == nil {
		return
	}

	now := time.Now()
	expired := false
	for market.ExpiryHeap.Len() > 0 {
		oldest := market.ExpiryHeap.Peek()
		if oldest == nil || oldest.Expiration.IsZero() || oldest.Expiration.After(now) {
			break
		}
		expired = true
		heap.Pop(market.ExpiryHeap)

		if oldest.IsBuyOrder {
			removeFromHeap(market.BuyHeap, oldest.ID)
		} else {
			removeFromHeap(market.SellHeap, oldest.ID)
		}

		oms.db.Model(oldest).Update("status", StatusExpired)
		oms.logger.Info("order expired",
			zap.Uint("id", oldest.ID),
			zap.String("symbol", symbol),
		)
	}

	if expired {
		oms.matchLocked(oms.db, market)
	}
}

func removeFromHeap(h *OrderHeap, orderID uint) {
	idx := h.FindIndex(orderID)
	if idx >= 0 {
		heap.Remove(h, idx)
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
