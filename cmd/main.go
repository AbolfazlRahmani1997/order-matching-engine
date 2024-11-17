package main

import (
	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"net/http"
	"orderMatchEngin/internal/core"
	"time"
)

func main() {

	dsn := "root:Ab@123456@tcp(localhost:3306)/order_engin?charset=utf8mb4&parseTime=True&loc=Local"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		panic("Failed to connect to the database!")
	}
	err = db.AutoMigrate(core.Order{})
	if err != nil {
		return
	}
	system := core.NewOrderMatchingSystem(*db)
	go system.MatchOrders()
	go system.CleanupExpiredOrders()

	router := gin.Default()

	router.POST("/buy", func(c *gin.Context) {
		var req struct {
			Price      float64 `json:"price"`
			Quantity   float64 `json:"quantity"`
			Expiration int     `json:"expiration"` // seconds
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		expiration := time.Now().Add(time.Duration(req.Expiration) * time.Second)
		orderID := system.AddOrder(req.Price, req.Quantity, true, expiration)
		c.JSON(http.StatusOK, gin.H{"message": "Buy order added", "order_id": orderID})
	})

	router.POST("/sell", func(c *gin.Context) {
		var req struct {
			Price      float64 `json:"price"`
			Quantity   float64 `json:"quantity"`
			Expiration int     `json:"expiration"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		expiration := time.Now().Add(time.Duration(req.Expiration) * time.Second)
		orderID := system.AddOrder(req.Price, req.Quantity, false, expiration)
		c.JSON(http.StatusOK, gin.H{"message": "Sell order added", "order_id": orderID})
	})

	err = router.Run(":8080")
	if err != nil {
		return
	}
}
