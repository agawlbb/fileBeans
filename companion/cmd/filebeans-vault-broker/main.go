package main

import (
	"context"
	"filebeans/companion/internal/service"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	syscall.Umask(0077)
	path := flag.String("config", "/etc/filebeans/vault-broker.json", "broker configuration")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.RunVaultBroker(ctx, *path); err != nil {
		log.Fatal("Vault broker stopped: configuration, memory policy, or mount operation failed")
	}
}
