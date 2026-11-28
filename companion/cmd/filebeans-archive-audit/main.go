package main

import (
	"context"
	"encoding/json"
	"filebeans/companion/internal/service"
	"flag"
	"log"
	"os"
)

func main() {
	p := flag.String("config", "/config/filesystem-trial.json", "private server configuration")
	plan := flag.String("reconcile-plan", "", "execute only the supplied archive journal IDs")
	flag.Parse()
	c, e := service.Load(*p)
	if e != nil {
		log.Fatal("Configuration unavailable")
	}
	s, e := service.New(c)
	if e != nil {
		log.Fatal("Service unavailable")
	}
	defer s.Close()
	if *plan != "" {
		f, e := os.Open(*plan)
		if e != nil {
			log.Fatal("Plan unavailable")
		}
		var p service.ArchiveRepairPlan
		e = json.NewDecoder(f).Decode(&p)
		f.Close()
		if e != nil {
			log.Fatal("Invalid plan")
		}
		report, e := s.ReconcileArchiveRetentions(context.Background(), p)
		json.NewEncoder(os.Stdout).Encode(report)
		if e != nil {
			log.Fatal(e)
		}
		return
	}
	if e = s.ArchiveAudit(os.Stdout); e != nil {
		log.Fatal("Archive metadata unavailable")
	}
}
