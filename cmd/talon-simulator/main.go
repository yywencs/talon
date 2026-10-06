package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/wen/opentalon/internal/scenario"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/simulatorhttp"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dataset := flag.String("dataset", "data/toolops-v1", "dataset path")
	id := flag.String("scenario", "mapping-regression-rollback-001", "scenario ID")
	listen := flag.String("listen", "127.0.0.1:8090", "local HTTP address")
	interval := flag.Duration("clock-interval", 20*time.Millisecond, "wall interval for one virtual tick while operations active")
	flag.Parse()
	if *interval <= 0 {
		return fmt.Errorf("clock-interval must be positive")
	}
	ds, err := scenario.LoadDataset(*dataset)
	if err != nil {
		return err
	}
	item, ok := ds.Find(*id)
	if !ok {
		return fmt.Errorf("scenario not found")
	}
	sim, err := simulator.New(item.Scenario)
	if err != nil {
		return err
	}
	offset := time.Duration(0)
	if item.Scenario.Clock.IncidentAt != "" {
		offset, err = time.ParseDuration(item.Scenario.Clock.IncidentAt)
	} else {
		for i, e := range item.Scenario.Timeline {
			d, x := time.ParseDuration(e.At)
			if x != nil {
				return x
			}
			if i == 0 || d < offset {
				offset = d
			}
		}
	}
	if err != nil {
		return err
	}
	if err = sim.Advance(context.Background(), offset); err != nil {
		return err
	}
	service, err := simulatorhttp.NewServer(sim, ds.Version)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: service, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	results := make(chan error, 2)
	go func() { results <- service.Clock(ctx, *interval) }()
	go func() { results <- server.Serve(listener) }()
	info := service.Info()
	fmt.Printf("simulator_url=http://%s environment_id=%s incident_id=%s dataset_version=%s virtual_time=%s\n", listener.Addr(), info.EnvironmentID, info.IncidentID, info.DatasetVersion, info.Now.Format(time.RFC3339))
	select {
	case <-ctx.Done():
	case err = <-results:
		if err == http.ErrServerClosed {
			err = nil
		}
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdown)
	return errors.Join(err, shutdownErr)
}
