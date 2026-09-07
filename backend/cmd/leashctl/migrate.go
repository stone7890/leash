package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/stone7890/leash/internal/migrate"
	"github.com/stone7890/leash/internal/store"
)

func open(ctx context.Context) (*store.Store, error) {
	c, cancel := withTimeout(ctx, 30*time.Second)
	defer cancel()
	return store.Connect(c, mongoURI(), mongoDB())
}

func cmdMigrate(ctx context.Context, args []string) error {
	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())

	if v, ok := flagValue(args, "--to"); ok {
		to, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("--to takes a version number, got %q", v)
		}
		c, cancel := withTimeout(ctx, 5*time.Minute)
		defer cancel()
		if err := migrate.Rollback(c, st.Migrator(), to); err != nil {
			return err
		}
		fmt.Printf("rolled back to version %d\n", to)
		return nil
	}

	c, cancel := withTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := migrate.Apply(c, st.Migrator()); err != nil {
		return err
	}
	all := migrate.All()
	fmt.Printf("schema is at version %d (%d migrations)\n", all[len(all)-1].Version, len(all))
	return nil
}
