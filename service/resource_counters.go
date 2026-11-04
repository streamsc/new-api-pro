package service

import (
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/common"
)

func InitResourceCounters() error {
	mode := os.Getenv("GATEWAY_REPLICA_MODE")
	if mode != "" && mode != "single" && mode != "cluster" {
		return fmt.Errorf("GATEWAY_REPLICA_MODE must be single or cluster")
	}
	if mode == "cluster" && (!common.RedisEnabled || common.RDB == nil) {
		return fmt.Errorf("cluster resource counters require shared Redis")
	}
	common.SysLog(fmt.Sprintf("Resource counters: shared_redis=%t; memory counters reset on restart", common.RedisEnabled))
	return nil
}
