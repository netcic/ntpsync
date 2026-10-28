// Command stress 是局域网 NTP 服务端的并发压测工具。
// 模拟多个客户端并发请求，验证服务端并发健壮性与无丢包。
//
// 用法：
//
//	go run ./cmd/stress -s 192.168.1.10:123 -c 50 -n 10
//
// 参数：-s 服务端地址，-c 并发客户端数，-n 每客户端请求次数。
package main

import (
	"flag"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"ntpsync/internal/ntp"
)

func main() {
	server := flag.String("s", "127.0.0.1:123", "服务端地址")
	clients := flag.Int("c", 50, "并发客户端数")
	each := flag.Int("n", 10, "每客户端请求次数")
	flag.Parse()

	total := *clients * *each
	var success, failed int64
	var sumDelay int64 // 纳秒累计
	var minOff, maxOff int64

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < *clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < *each; j++ {
				off, delay, err := ntp.Query(*server, 3*time.Second)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					continue
				}
				atomic.AddInt64(&success, 1)
				atomic.AddInt64(&sumDelay, int64(delay))
				o := int64(off)
				if o < minOff || minOff == 0 {
					atomic.StoreInt64(&minOff, o)
				}
				if o > maxOff {
					atomic.StoreInt64(&maxOff, o)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	fmt.Printf("压测结果（服务端 %s）\n", *server)
	fmt.Printf("  总请求: %d  成功: %d  失败: %d\n", total, success, failed)
	fmt.Printf("  耗时: %s  吞吐: %.0f req/s\n", elapsed, float64(total)/elapsed.Seconds())
	if success > 0 {
		avgDelay := time.Duration(sumDelay / success)
		fmt.Printf("  平均时延: %v  偏差范围: [%v, %v]\n",
			avgDelay, time.Duration(minOff), time.Duration(maxOff))
	}
	if failed > 0 {
		log.Fatalf("存在 %d 次失败，请检查服务端可达性与防火墙。", failed)
	}
	fmt.Println("  ✅ 并发无丢包，服务端健壮")
}
