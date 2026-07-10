package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/jupiter/gold-proto/matching/v1"
	"github.com/jupiter/order-matching-engine/config"
	"github.com/jupiter/order-matching-engine/internal/engine"
	"github.com/jupiter/order-matching-engine/internal/grpcserver"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	logger := newLogger(cfg.Log.Level)
	defer logger.Sync()

	db, err := initDB(cfg, logger)
	if err != nil {
		logger.Fatal("failed to connect to database", zap.Error(err))
	}

	if err := db.AutoMigrate(&engine.Order{}); err != nil {
		logger.Fatal("failed to run migrations", zap.Error(err))
	}

	oms := engine.NewOrderMatchingSystem(db, logger)

	if err := oms.LoadFromDB(); err != nil {
		logger.Error("failed to load orders from DB", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go oms.StartExpirationLoop(ctx, "GOLD_IRR")

	handler := engine.NewHandler(oms, logger)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "order-matching-engine"})
	})

	api := router.Group("/api/v1")
	{
		api.POST("/orders", handler.PlaceOrder)
		api.GET("/orders/:id", handler.GetOrder)
		api.DELETE("/orders/:id", handler.CancelOrder)
		api.GET("/orderbook/:symbol", handler.GetOrderBook)
	}

	// gRPC server
	grpcSrv := grpc.NewServer()
	matchingSrv := grpcserver.New(oms, logger)
	matchingv1.RegisterMatchingEngineServer(grpcSrv, matchingSrv)

	grpcPort := cfg.Server.Port + 1
	grpcAddr := fmt.Sprintf(":%d", grpcPort)
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		logger.Fatal("failed to listen gRPC", zap.Error(err))
	}

	go func() {
		logger.Info("gRPC server starting", zap.String("addr", grpcAddr))
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Fatal("gRPC server failed", zap.Error(err))
		}
	}()

	// HTTP server
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Info("HTTP server starting", zap.String("addr", addr))

	go func() {
		if err := router.Run(addr); err != nil {
			logger.Fatal("server failed", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down order-matching-engine...")
	grpcSrv.GracefulStop()
	cancel()
}

func initDB(cfg *config.Config, logger *zap.Logger) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.Database.DSN()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)

	return db, nil
}

func newLogger(level string) *zap.Logger {
	var lvl zapcore.Level
	switch level {
	case "debug":
		lvl = zap.DebugLevel
	case "warn":
		lvl = zap.WarnLevel
	case "error":
		lvl = zap.ErrorLevel
	default:
		lvl = zap.InfoLevel
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	cfg.EncoderConfig.TimeKey = "timestamp"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	logger, _ := cfg.Build()
	return logger
}
