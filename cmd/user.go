package main

import (
	"fmt"
	"strings"

	"minimalpanel/internal/auth"
	"minimalpanel/internal/conf"
)

func createOrUpdateUser(configPath, username, password string) error {
	if err := validateUserInput(username, password); err != nil {
		return err
	}
	if err := conf.LoadConfig(configPath); err != nil {
		return err
	}
	return createUser(username, password)
}

func validateUserInput(username, password string) error {
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("username cannot be empty")
	}
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	return nil
}

func createUser(username, password string) error {
	return auth.NewUser(strings.TrimSpace(username), password)
}
