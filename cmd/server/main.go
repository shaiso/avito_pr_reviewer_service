package main

import (
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/shaiso/pr-reviewer/internal/api"
)

func main() {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Fatal("DB_DSN is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal("open db: %v", err)
	}
	defer db.Close()

	if dsn != "" {
		if err := db.Ping(); err != nil {
			log.Fatalf("ping db: %v", err)
		}
		log.Println("db connected")
	} else {
		log.Println("db NOT connected (DSN empty) — ok for now, will configure in docker-compose")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	srv := api.NewServer(db)
	srv.RegisterRoutes(mux)

	handler := loggingMiddleware(mux)

	addr := ":8080"

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("server listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("listen and serve: %v", err)
	}
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
