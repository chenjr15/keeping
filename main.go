package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

var usage = `
Usage:

    ping [-c count] [-i interval] [-t timeout] [--privileged] [-k  statistic interval] host

Examples:

    # ping google continuously
    ping www.google.com

    # ping google 5 times
    ping -c 5 www.google.com

    # ping google 5 times at 500ms intervals
    ping -c 5 -i 500ms www.google.com

    # ping google for 10 seconds
    ping -t 10s www.google.com

    # Send a privileged raw ICMP ping
    sudo ping --privileged www.google.com

    # Send ICMP messages with a 100-byte payload
    ping -s 100 1.1.1.1
`

func main() {
	timeout := flag.Duration("t", time.Second*100000, "")
	interval := flag.Duration("i", time.Second, "")
	statisticInterval := flag.Duration("k", 0, "")
	count := flag.Int("c", -1, "")
	size := flag.Int("s", 24, "")
	ttl := flag.Int("l", 64, "TTL")
	privileged := flag.Bool("privileged", false, "")
	flag.Usage = func() {
		fmt.Print(usage)
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		return
	}

	host := flag.Arg(0)
	pinger, err := probing.NewPinger(host)
	if err != nil {
		fmt.Println("ERROR:", err)
		return
	}

	// listen for ctrl-C signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			pinger.Stop()
		}
	}()
	counter := &Counter{}
	mu := &sync.Mutex{}

	pinger.OnRecv = func(pkt *probing.Packet) {
		counter.UpdateSync(mu, int64(pkt.Rtt))
		fmt.Printf("%d bytes from %s: icmp_seq=%d time=%v ttl=%v\n",
			pkt.Nbytes, pkt.IPAddr, pkt.Seq, pkt.Rtt, pkt.TTL)
	}
	pinger.OnDuplicateRecv = func(pkt *probing.Packet) {
		fmt.Printf("%d bytes from %s: icmp_seq=%d time=%v ttl=%v (DUP!)\n",
			pkt.Nbytes, pkt.IPAddr, pkt.Seq, pkt.Rtt, pkt.TTL)
	}
	pinger.OnFinish = func(stats *probing.Statistics) {
		fmt.Printf("\n--- %s ping statistics ---\n", stats.Addr)
		fmt.Printf("%d packets transmitted, %d packets received, %d duplicates, %v%% packet loss\n",
			stats.PacketsSent, stats.PacketsRecv, stats.PacketsRecvDuplicates, stats.PacketLoss)
		fmt.Printf("round-trip min/avg/max/stddev = %v/%v/%v/%v\n",
			stats.MinRtt, stats.AvgRtt, stats.MaxRtt, stats.StdDevRtt)
	}

	pinger.Count = *count
	pinger.Size = *size
	pinger.Interval = *interval
	pinger.Timeout = *timeout
	pinger.TTL = *ttl
	pinger.SetPrivileged(*privileged)

	fmt.Printf("PING %s (%s):\n", pinger.Addr(), pinger.IPAddr())

	done := make(chan struct{})
	go func() {
		defer close(done)
		err = pinger.Run()
		if err != nil {
			fmt.Println("Failed to ping target host:", err)
		}

		done <- struct{}{}
	}()
	// wait for stop
	if *statisticInterval == time.Duration(0) {
		<-done
		return
	}

	logIntervalTimer := time.NewTicker(*statisticInterval)
	defer logIntervalTimer.Stop()
	for exit := false; !exit; {
		select {
		case <-logIntervalTimer.C:
			// 	统计一波并清除
			mu.Lock()
			fmt.Println(counter.String())
			counter.Reset()
			mu.Unlock()
		case <-done:
			exit = true
			break
		}
	}
}

type Counter struct {
	Count    int64
	Min      int64
	Max      int64
	Avg      int64
	StdDevM2 int64
}

func (cnt *Counter) String() string {
	return fmt.Sprintf("%d packets,RTT min/avg/max/stddev = %v/%v/%v/%v", cnt.Count,
		time.Duration(cnt.Min), time.Duration(cnt.Avg), time.Duration(cnt.Max), time.Duration(cnt.StdDevM2))
}
func (cnt *Counter) Reset() {
	cnt.Count = 0
	cnt.Min = 0
	cnt.Max = 0
	cnt.Avg = 0
	cnt.StdDevM2 = 0
}

func (cnt *Counter) UpdateSync(mu *sync.Mutex, val int64) {
	mu.Lock()
	defer mu.Unlock()
	cnt.Update(val)
}
func (cnt *Counter) Update(val int64) {

	if cnt.Count == 1 || val < cnt.Min {
		cnt.Min = val
	}

	if val > cnt.Max {
		cnt.Max = val
	}
	cnt.Count++
	pktCount := cnt.Count
	// ref: pro-bing/ping.go#Pinger.updateStatistics
	delta := val - cnt.Avg
	cnt.Avg += delta / pktCount
	delta2 := val - cnt.Avg
	cnt.StdDevM2 += delta * delta2
	cnt.StdDevM2 = int64(math.Sqrt(float64(cnt.StdDevM2 / pktCount)))
}
