package core

import (
	"container/heap"
	"fmt"
	"gorm.io/gorm"
	"log"
	"sync"
	"time"
)

type Order struct {
	ID         int     `gorm:"primaryKey"`
	Price      float64 `gorm:"not null"`
	Quantity   float64 `gorm:"not null"`
	Fill       float64 `gorm:"not null"`
	Status     string  `gorm:"type:varchar(20);default:'pending'"`
	Expiration time.Time
	Index      int
	IsBuyOrder bool
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

type OrderMatchingSystem struct {
	buyHeap    MaxHeap
	sellHeap   MinHeap
	expiryHeap MinHeap
	lock       sync.Mutex
	orderID    int
	Db         gorm.DB
}

func NewOrderMatchingSystem(db gorm.DB) *OrderMatchingSystem {
	return &OrderMatchingSystem{
		buyHeap:    MaxHeap{MinHeap: MinHeap{}},
		sellHeap:   MinHeap{},
		expiryHeap: MinHeap{},
		Db:         db,
	}
}

func (oms *OrderMatchingSystem) AddOrder(price, quantity float64, isBuyOrder bool, expiration time.Time) int {
	oms.lock.Lock()
	defer oms.lock.Unlock()

	order := &Order{
		Price:      price,
		Quantity:   quantity,
		Expiration: expiration,
		IsBuyOrder: isBuyOrder,
		Status:     "created",
	}
	oms.Db.Create(order)

	heap.Push(&oms.expiryHeap, order)
	if isBuyOrder {
		heap.Push(&oms.buyHeap, order)
	} else {
		heap.Push(&oms.sellHeap, order)
	}

	fmt.Printf("Added order: %+v\n", order)
	return order.ID
}

func (oms *OrderMatchingSystem) MatchOrders() {
	for {
		oms.lock.Lock()
		if oms.buyHeap.Len() > 0 && oms.sellHeap.Len() > 0 {
			buy := oms.buyHeap.Peek()
			sell := oms.sellHeap.Peek()
			buy.Status = "in_process"
			sell.Status = "in_process"
			if buy.Price >= sell.Price {
				quantity := min(buy.Quantity, sell.Quantity)
				profit := buy.Price - sell.Price

				fmt.Printf("Matched Order! Buy ID: %d, Sell ID: %d, Quantity: %.2f, Profit: %.2f\n",
					buy.ID, sell.ID, quantity, profit*quantity)
				buy.Fill += quantity
				sell.Fill += quantity
				buy.Quantity -= quantity
				sell.Quantity -= quantity
				go oms.Db.Save(sell)
				go oms.Db.Save(buy)
				if buy.Quantity == 0 {
					heap.Pop(&oms.buyHeap)
					go oms.Db.Model(buy).Where("id = ?", buy.ID).Update("status", "completed")

				}
				if sell.Quantity == 0 {
					heap.Pop(&oms.sellHeap)
					go oms.Db.Model(sell).Where("id = ?", sell.ID).Update("status", "completed")
				}
			} else {
				break
			}
		}

		oms.lock.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
}

func (oms *OrderMatchingSystem) UpdateOrder(orderID int) {
	result := oms.Db.Model(&Order{}).Where("id = ?", orderID).Update("status", "in_process")
	if result.Error != nil {
		fmt.Println(result.Error)
	}
}
func (oms *OrderMatchingSystem) UpdateFillOrder(orderID Order) {
	result := oms.Db.Model(&Order{}).Where("id = ?", orderID.ID).Update("fill", orderID.Fill)
	if result.Error != nil {
		fmt.Println(result.Error)
	}
}

func (oms *OrderMatchingSystem) CleanupExpiredOrders() {
	for {
		oms.lock.Lock()

		now := time.Now()
		for oms.expiryHeap.Len() > 0 {
			expiredOrder := oms.expiryHeap.Peek()
			if expiredOrder.Expiration.After(now) {
				break
			}

			heap.Pop(&oms.expiryHeap)
			if expiredOrder.IsBuyOrder {
				oms.removeFromHeap(&oms.buyHeap.MinHeap, expiredOrder)
			} else {
				oms.removeFromHeap(&oms.sellHeap, expiredOrder)
			}

			fmt.Printf("Order ID %d expired and removed\n", expiredOrder.ID)
		}

		oms.lock.Unlock()
	}
}

func (oms *OrderMatchingSystem) removeFromHeap(h *MinHeap, order *Order) {
	for i, o := range *h {
		if o.ID == order.ID && order.Status == "created" {

			result := oms.Db.Model(order).Where("id = ?", order.ID).Update("status", "expired")
			if result.Error != nil {
				log.Fatalf("Error updating status: %v", result.Error)
			}

			oms.Db.Transaction(func(tx *gorm.DB) error {
				heap.Remove(h, i)

				return nil
			})
			return
		}
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
