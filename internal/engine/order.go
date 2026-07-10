package engine

import (
	"container/heap"
	"time"
)

type OrderStatus string

const (
	StatusCreated  OrderStatus = "created"
	StatusPartial  OrderStatus = "partial"
	StatusFilled   OrderStatus = "filled"
	StatusCanceled OrderStatus = "canceled"
	StatusExpired  OrderStatus = "expired"
)

type Order struct {
	ID         uint        `gorm:"primaryKey" json:"id"`
	UserID     uint        `gorm:"index;not null" json:"user_id"`
	Symbol     string      `gorm:"index;not null;size:20" json:"symbol"`
	IsBuyOrder bool        `gorm:"not null" json:"is_buy_order"`
	Price      float64     `gorm:"not null" json:"price"`
	Quantity   float64     `gorm:"not null" json:"quantity"`
	FilledQty  float64     `gorm:"default:0" json:"filled_qty"`
	Status     OrderStatus `gorm:"default:created;size:20" json:"status"`
	BotID      string      `gorm:"size:50" json:"bot_id,omitempty"`
	Expiration time.Time   `json:"expiration,omitempty"`
	Index      int         `gorm:"-" json:"-"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

func (o Order) Remaining() float64 {
	return o.Quantity - o.FilledQty
}

func (o Order) IsActive() bool {
	return o.Status == StatusCreated || o.Status == StatusPartial
}

type OrderHeap struct {
	orders []*Order
	less   func(a, b *Order) bool
}

func NewBuyHeap() *OrderHeap {
	return &OrderHeap{less: func(a, b *Order) bool {
		if a.Price == b.Price {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.Price > b.Price
	}}
}

func NewSellHeap() *OrderHeap {
	return &OrderHeap{less: func(a, b *Order) bool {
		if a.Price == b.Price {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.Price < b.Price
	}}
}

func NewExpiryHeap() *OrderHeap {
	return &OrderHeap{less: func(a, b *Order) bool {
		return a.Expiration.Before(b.Expiration)
	}}
}

func (h OrderHeap) Len() int           { return len(h.orders) }
func (h OrderHeap) Less(i, j int) bool { return h.less(h.orders[i], h.orders[j]) }
func (h OrderHeap) Swap(i, j int) {
	h.orders[i], h.orders[j] = h.orders[j], h.orders[i]
	h.orders[i].Index = i
	h.orders[j].Index = j
}

func (h *OrderHeap) Push(x interface{}) {
	order := x.(*Order)
	order.Index = len(h.orders)
	h.orders = append(h.orders, order)
}

func (h *OrderHeap) Pop() interface{} {
	n := len(h.orders)
	order := h.orders[n-1]
	order.Index = -1
	h.orders = h.orders[:n-1]
	return order
}

func (h *OrderHeap) Peek() *Order {
	if len(h.orders) == 0 {
		return nil
	}
	return h.orders[0]
}

func (h *OrderHeap) FindIndex(orderID uint) int {
	for i, o := range h.orders {
		if o.ID == orderID {
			return i
		}
	}
	return -1
}

var _ heap.Interface = (*OrderHeap)(nil)
