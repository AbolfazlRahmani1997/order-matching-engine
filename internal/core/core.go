package core

import (
	"container/heap"
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	_ "log"
	"sync"
	"testing"
	"time"
)

type Order struct {
	ID          int     `gorm:"primaryKey"`
	Price       float64 `gorm:"not null"`
	Quantity    float64 `gorm:"not null"`
	Fill        float64 `gorm:"not null"`
	Status      string  `gorm:"type:varchar(20);default:'pending'"`
	Expiration  time.Time
	Index       int
	IsBuyOrder  bool
	Market      string `gorm:"type:varchar(20);not null"`
	TakeProfit  *float64
	StopLoss    *float64
	MarginOrder bool
	Leverage    float64
	UserID      uint
}
type Position struct {
	ID         uint `gorm:"primaryKey"`
	UserID     uint
	Market     string
	EntryPrice float64
	Quantity   float64
	Leverage   float64
	IsLong     bool
	Status     string
	TP         *float64
	SL         *float64
	CreatedAt  time.Time
	ClosedAt   *time.Time
	ExitPrice  *float64
	PnL        *float64
}

type Match struct {
	AskID    int
	BidID    int
	Price    float64
	Quantity float64
	Profit   float64
}

type MinHeap []*Order

func (h MinHeap) Len() int           { return len(h) }
func (h MinHeap) Less(i, j int) bool { return h[i].Price < h[j].Price }
func (h MinHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].Index = i
	h[j].Index = j
}

func (h *MinHeap) Push(x interface{}) {
	n := len(*h)
	order := x.(*Order)
	order.Index = n
	*h = append(*h, order)
}

func (h *MinHeap) Pop() interface{} {
	old := *h
	n := len(old)
	order := old[n-1]
	order.Index = -1
	*h = old[0 : n-1]
	return order
}

func (h *MinHeap) Peek() *Order {
	if len(*h) == 0 {
		return nil
	}
	return (*h)[0]
}

type MaxHeap struct {
	MinHeap
}

func (h MaxHeap) Less(i, j int) bool { return h.MinHeap[i].Price > h.MinHeap[j].Price }

type Market struct {
	Symbol     string
	BuyHeap    MaxHeap
	SellHeap   MinHeap
	ExpiryHeap MinHeap
	LastPrice  float64
	BuyVolume  float64
	SellVolume float64
}

type OrderMatchingSystem struct {
	markets map[string]*Market
	lock    sync.Mutex
	db      *gorm.DB
}

func NewOrderMatchingSystem(db *gorm.DB) *OrderMatchingSystem {
	oms := &OrderMatchingSystem{
		markets: make(map[string]*Market),
		db:      db,
	}
	return oms
}

func (oms *OrderMatchingSystem) AddOrderToMarket(symbol string, price, quantity float64, isBuy bool, expiration time.Time, userID uint, isMargin bool, leverage float64, tp, sl *float64) int {
	oms.lock.Lock()
	defer oms.lock.Unlock()

	market, exists := oms.markets[symbol]
	if !exists {
		market = &Market{
			Symbol:     symbol,
			BuyHeap:    MaxHeap{MinHeap: MinHeap{}},
			SellHeap:   MinHeap{},
			ExpiryHeap: MinHeap{},
		}
		oms.markets[symbol] = market
		go oms.matchingLoopForMarket(symbol)
		go oms.expirationLoopForMarket(symbol)
		go oms.marginMonitor(symbol)
	}

	order := &Order{
		Price:       price,
		Quantity:    quantity,
		IsBuyOrder:  isBuy,
		Expiration:  expiration,
		Status:      "created",
		Market:      symbol,
		UserID:      userID,
		MarginOrder: isMargin,
		Leverage:    leverage,
		TakeProfit:  tp,
		StopLoss:    sl,
	}
	oms.db.Create(order)

	heap.Push(&market.ExpiryHeap, order)
	if isBuy {
		heap.Push(&market.BuyHeap, order)
	} else {
		heap.Push(&market.SellHeap, order)
	}

	fmt.Printf("Added order to %s: %+v\n", symbol, order)
	return order.ID
}

func (oms *OrderMatchingSystem) GetOpenOrders(symbol string) (buyOrders []*Order, sellOrders []*Order) {
	oms.lock.Lock()
	defer oms.lock.Unlock()

	market, exists := oms.markets[symbol]
	if !exists {
		return nil, nil
	}

	for _, o := range market.BuyHeap.MinHeap {
		if o.Quantity > 0 && (o.Status == "created" || o.Status == "in_process") {
			buyOrders = append(buyOrders, o)
		}
	}
	for _, o := range market.SellHeap {
		if o.Quantity > 0 && (o.Status == "created" || o.Status == "in_process") {
			sellOrders = append(sellOrders, o)
		}
	}
	return
}

func (oms *OrderMatchingSystem) matchingLoopForMarket(symbol string) {
	for {
		time.Sleep(100 * time.Millisecond)
		oms.lock.Lock()
		market := oms.markets[symbol]
		if market == nil {
			oms.lock.Unlock()
			continue
		}
		for market.BuyHeap.Len() > 0 && market.SellHeap.Len() > 0 {
			buy := market.BuyHeap.Peek()
			sell := market.SellHeap.Peek()
			if buy.Price >= sell.Price {
				quantity := min(buy.Quantity, sell.Quantity)
				profit := buy.Price - sell.Price
				match := Match{
					AskID:    sell.ID,
					BidID:    buy.ID,
					Price:    sell.Price,
					Quantity: quantity,
					Profit:   profit * quantity,
				}
				fmt.Printf("Matched [%s]: %+v\n", symbol, match)
				market.LastPrice = match.Price
				market.BuyVolume += match.Quantity
				market.SellVolume += match.Quantity

				buy.Fill += quantity
				sell.Fill += quantity
				buy.Quantity -= quantity
				sell.Quantity -= quantity

				oms.db.Save(sell)
				oms.db.Save(buy)

				if buy.Quantity == 0 {
					heap.Pop(&market.BuyHeap)
					oms.db.Model(buy).Update("status", "completed")
				}
				if sell.Quantity == 0 {
					heap.Pop(&market.SellHeap)
					oms.db.Model(sell).Update("status", "completed")
				}
			} else {
				break
			}
		}
		oms.lock.Unlock()
	}
}
func (oms *OrderMatchingSystem) CancelOrder(orderID int) error {
	oms.lock.Lock()
	defer oms.lock.Unlock()

	var order Order
	if err := oms.db.First(&order, orderID).Error; err != nil {
		return fmt.Errorf("order not found: %v", err)
	}

	if order.Status != "created" && order.Status != "in_process" {
		return fmt.Errorf("cannot cancel order with status: %s", order.Status)
	}

	if err := oms.db.Model(&order).Update("status", "cancelled").Error; err != nil {
		return fmt.Errorf("failed to update status: %v", err)
	}

	market := oms.markets[order.Market]
	if market == nil {
		return fmt.Errorf("market not found")
	}

	if order.IsBuyOrder {
		oms.removeFromHeap(&market.BuyHeap.MinHeap, &order)
	} else {
		oms.removeFromHeap(&market.SellHeap, &order)
	}
	oms.removeFromHeap(&market.ExpiryHeap, &order)

	fmt.Printf("Order ID %d cancelled\n", order.ID)
	return nil
}

func (oms *OrderMatchingSystem) expirationLoopForMarket(symbol string) {
	for {
		time.Sleep(1 * time.Second)
		now := time.Now()
		oms.lock.Lock()
		market := oms.markets[symbol]
		if market == nil {
			oms.lock.Unlock()
			continue
		}
		for market.ExpiryHeap.Len() > 0 {
			expiredOrder := market.ExpiryHeap.Peek()
			if expiredOrder.Expiration.After(now) {
				break
			}
			heap.Pop(&market.ExpiryHeap)
			if expiredOrder.IsBuyOrder {
				oms.removeFromHeap(&market.BuyHeap.MinHeap, expiredOrder)
			} else {
				oms.removeFromHeap(&market.SellHeap, expiredOrder)
			}
			oms.db.Model(expiredOrder).Update("status", "expired")
			fmt.Printf("Order ID %d in %s expired and removed\n", expiredOrder.ID, symbol)
		}
		oms.lock.Unlock()
	}
}

func (oms *OrderMatchingSystem) removeFromHeap(h *MinHeap, order *Order) {
	for i, o := range *h {
		if o.ID == order.ID {
			heap.Remove(h, i)
			return
		}
	}
}

func (oms *OrderMatchingSystem) monitorTPandSL(symbol string) {
	for {
		time.Sleep(500 * time.Millisecond)
		oms.lock.Lock()
		market := oms.markets[symbol]
		if market == nil {
			oms.lock.Unlock()
			continue
		}
		lastPrice := market.LastPrice
		for _, order := range append(market.BuyHeap.MinHeap, market.SellHeap...) {
			if order.Status != "created" {
				continue
			}
			// چک کردن TP
			if order.TakeProfit != nil {
				if order.IsBuyOrder && lastPrice >= *order.TakeProfit {
					oms.CancelOrder(order.ID)
				} else if !order.IsBuyOrder && lastPrice <= *order.TakeProfit {
					oms.CancelOrder(order.ID)
				}
			}
			// چک کردن SL
			if order.StopLoss != nil {
				if order.IsBuyOrder && lastPrice <= *order.StopLoss {
					oms.CancelOrder(order.ID)
				} else if !order.IsBuyOrder && lastPrice >= *order.StopLoss {
					oms.CancelOrder(order.ID)
				}
			}
		}
		oms.lock.Unlock()
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func TestOrderMatchingSystem(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect database: %v", err)
	}
	db.AutoMigrate(&Order{})

	oms := NewOrderMatchingSystem(db)
	market := "BTC_USDT"

	//oms.AddOrderToMarket(market, 30000, 1, true, time.Now().Add(1*time.Hour),1,false,0,1.2,0)  // Buy Order
	//oms.AddOrderToMarket(market, 29900, 1, false, time.Now().Add(1*time.Hour)) // Sell Order

	time.Sleep(300 * time.Millisecond)

	buyOrders, sellOrders := oms.GetOpenOrders(market)
	if len(buyOrders) != 0 || len(sellOrders) != 0 {
		t.Errorf("Expected no open orders, got %d buy and %d sell", len(buyOrders), len(sellOrders))
	}

	lastPrice := oms.markets[market].LastPrice
	if lastPrice != 29900 {
		t.Errorf("Expected last price 29900, got %f", lastPrice)
	}

	buyVol := oms.markets[market].BuyVolume
	sellVol := oms.markets[market].SellVolume
	if buyVol != 1 || sellVol != 1 {
		t.Errorf("Expected volumes 1, got buy: %f, sell: %f", buyVol, sellVol)
	}
}

func (oms *OrderMatchingSystem) marginMonitor(symbol string) {
	for {
		time.Sleep(1 * time.Second)
		oms.lock.Lock()
		var positions []Position
		market := oms.markets[symbol]
		if market == nil {
			oms.lock.Unlock()
			continue
		}
		oms.db.Where("market = ? AND status = ?", symbol, "open").Find(&positions)
		price := market.LastPrice
		now := time.Now()
		for _, pos := range positions {
			exit := false
			var reason string
			// Check TP/SL
			if pos.TP != nil && ((pos.IsLong && price >= *pos.TP) || (!pos.IsLong && price <= *pos.TP)) {
				exit = true
				reason = "TP"
			}
			if pos.SL != nil && ((pos.IsLong && price <= *pos.SL) || (!pos.IsLong && price >= *pos.SL)) {
				exit = true
				reason = "SL"
			}
			if exit {
				pnl := calculatePnL(pos.IsLong, pos.EntryPrice, price, pos.Quantity)
				pos.Status = "closed"
				pos.ClosedAt = &now
				pos.ExitPrice = &price
				pos.PnL = &pnl
				oms.db.Save(&pos)
				fmt.Printf("Position closed by %s: %+v\n", reason, pos)
				continue
			}
			// Liquidation
			liqThreshold := pos.EntryPrice / pos.Leverage * 0.95
			if (pos.IsLong && price <= pos.EntryPrice-liqThreshold) || (!pos.IsLong && price >= pos.EntryPrice+liqThreshold) {
				pnl := calculatePnL(pos.IsLong, pos.EntryPrice, price, pos.Quantity)
				pos.Status = "liquidated"
				pos.ClosedAt = &now
				pos.ExitPrice = &price
				pos.PnL = &pnl
				oms.db.Save(&pos)
				fmt.Printf("Position liquidated: %+v\n", pos)
			}
		}
		oms.lock.Unlock()
	}
}

func calculatePnL(isLong bool, entry, exit, qty float64) float64 {
	if isLong {
		return (exit - entry) * qty
	}
	return (entry - exit) * qty
}
