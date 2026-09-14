package cache

import (
	"context"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

var Ctx = context.Background()

func InitRedis() (*redis.Client, error) {
	redisURL := os.Getenv("REDIS_URL")

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}

	rdb := redis.NewClient(opt)

	// Verificar conexión con un Ping
	status := rdb.Ping(Ctx)
	if status.Err() != nil {
		return nil, status.Err()
	}

	fmt.Println("¡Conectado exitosamente a Redis desde Go!")
	return rdb, nil
}
