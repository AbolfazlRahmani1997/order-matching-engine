package grpcserver

import (
	"context"
	"time"

	"github.com/jupiter/gold-proto/matching/v1"
	"github.com/jupiter/order-matching-engine/internal/engine"
	"go.uber.org/zap"
)

type MatchingServer struct {
	matchingv1.UnimplementedMatchingEngineServer
	engine *engine.OrderMatchingSystem
	logger *zap.Logger
}

func New(eng *engine.OrderMatchingSystem, logger *zap.Logger) *MatchingServer {
	return &MatchingServer{engine: eng, logger: logger}
}

func (s *MatchingServer) PlaceOrder(ctx context.Context, req *matchingv1.PlaceOrderRequest) (*matchingv1.PlaceOrderResponse, error) {
	var expiration time.Time
	if req.ExpireSec > 0 {
		expiration = time.Now().Add(time.Duration(req.ExpireSec) * time.Second)
	}

	result, err := s.engine.AddOrder(&engine.OrderRequest{
		UserID:     uint(req.UserId),
		Symbol:     req.Symbol,
		IsBuyOrder: req.IsBuyOrder,
		Price:      req.Price,
		Quantity:   req.Quantity,
		BotID:      req.BotId,
		Expiration: expiration,
	})
	if err != nil {
		return nil, err
	}

	resp := &matchingv1.PlaceOrderResponse{
		Order:   toProtoOrder(result.Order),
		Matches: make([]*matchingv1.Match, len(result.Matches)),
	}
	for i, m := range result.Matches {
		resp.Matches[i] = &matchingv1.Match{
			BuyId:    uint32(m.BuyID),
			SellId:   uint32(m.SellID),
			Price:    m.Price,
			Quantity: m.Quantity,
		}
	}
	return resp, nil
}

func (s *MatchingServer) GetOrder(ctx context.Context, req *matchingv1.GetOrderRequest) (*matchingv1.GetOrderResponse, error) {
	order, err := s.engine.GetOrder(uint(req.OrderId))
	if err != nil {
		return nil, err
	}
	return &matchingv1.GetOrderResponse{Order: toProtoOrder(order)}, nil
}

func (s *MatchingServer) CancelOrder(ctx context.Context, req *matchingv1.CancelOrderRequest) (*matchingv1.CancelOrderResponse, error) {
	success := true
	if _, err := s.engine.CancelOrder(uint(req.OrderId)); err != nil {
		success = false
	}
	return &matchingv1.CancelOrderResponse{Success: success}, nil
}

func (s *MatchingServer) GetOrderBook(ctx context.Context, req *matchingv1.GetOrderBookRequest) (*matchingv1.GetOrderBookResponse, error) {
	bids, asks := s.engine.GetOrderBook(req.Symbol)
	lastPrice := s.engine.GetLastPrice(req.Symbol)

	resp := &matchingv1.GetOrderBookResponse{
		Symbol:    req.Symbol,
		LastPrice: lastPrice,
		Bids:      make([]*matchingv1.OrderBookItem, len(bids)),
		Asks:      make([]*matchingv1.OrderBookItem, len(asks)),
	}
	for i, b := range bids {
		resp.Bids[i] = &matchingv1.OrderBookItem{
			Price:     b.Price,
			Quantity:  b.Quantity,
			FilledQty: b.FilledQty,
		}
	}
	for i, a := range asks {
		resp.Asks[i] = &matchingv1.OrderBookItem{
			Price:     a.Price,
			Quantity:  a.Quantity,
			FilledQty: a.FilledQty,
		}
	}
	return resp, nil
}

func toProtoOrder(o *engine.Order) *matchingv1.Order {
	return &matchingv1.Order{
		Id:         uint32(o.ID),
		UserId:     uint32(o.UserID),
		Symbol:     o.Symbol,
		IsBuyOrder: o.IsBuyOrder,
		Price:      o.Price,
		Quantity:   o.Quantity,
		FilledQty:  o.FilledQty,
		Status:     string(o.Status),
		BotId:      o.BotID,
	}
}
