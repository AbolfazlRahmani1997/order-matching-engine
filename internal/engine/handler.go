package engine

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type Handler struct {
	engine *OrderMatchingSystem
	logger *zap.Logger
}

func NewHandler(engine *OrderMatchingSystem, logger *zap.Logger) *Handler {
	return &Handler{engine: engine, logger: logger}
}

type PlaceOrderRequest struct {
	UserID     uint    `json:"user_id" binding:"required"`
	Symbol     string  `json:"symbol" binding:"required"`
	IsBuyOrder bool    `json:"is_buy_order"`
	Price      float64 `json:"price" binding:"required,gt=0"`
	Quantity   float64 `json:"quantity" binding:"required,gt=0"`
	ExpireSec  int64   `json:"expire_sec"`
}

func (h *Handler) PlaceOrder(c *gin.Context) {
	var req PlaceOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var expiration time.Time
	if req.ExpireSec > 0 {
		expiration = time.Now().Add(time.Duration(req.ExpireSec) * time.Second)
	}

	result, err := h.engine.AddOrder(&OrderRequest{
		UserID:     req.UserID,
		Symbol:     req.Symbol,
		IsBuyOrder: req.IsBuyOrder,
		Price:      req.Price,
		Quantity:   req.Quantity,
		Expiration: expiration,
	})
	if err != nil {
		h.logger.Error("failed to place order", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, result)
}

func (h *Handler) GetOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	order, err := h.engine.GetOrder(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	c.JSON(http.StatusOK, order)
}

func (h *Handler) CancelOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
		return
	}

	result, err := h.engine.CancelOrder(uint(id))
	if err != nil {
		h.logger.Error("failed to cancel order", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "order canceled",
		"matches": result.Matches,
	})
}

func (h *Handler) GetOrderBook(c *gin.Context) {
	symbol := c.Param("symbol")
	if symbol == "" {
		symbol = "GOLD_IRR"
	}

	bids, asks := h.engine.GetOrderBook(symbol)
	c.JSON(http.StatusOK, gin.H{
		"symbol": symbol,
		"bids":   bids,
		"asks":   asks,
	})
}
