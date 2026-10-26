package service

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func runCommand(program string, arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, program, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %s: %w", program, arguments, output, err)
	}
	return nil
}
