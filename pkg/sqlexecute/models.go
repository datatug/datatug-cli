package sqlexecute

import (
	"errors"
	"fmt"
	"time"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/strongo/validation"
)

// Response holds execute response
type Response struct {
	Duration time.Duration      `json:"durationNanoseconds"`
	Commands []*CommandResponse `json:"commands"`
}

// CommandResponse defiens command response
type CommandResponse struct {
	Items []CommandResponseItem `json:"items"`
}

// CommandResponseItem defines response item
type CommandResponseItem struct {
	Type  string      `json:"type"` // e.g. recordset
	Value interface{} `json:"value"`
}

// Request defines what needs to be executed
type Request struct {
	ID       string           `json:"id"`
	Project  string           `json:"project"`
	Commands []RequestCommand `json:"commands"`
}

// Validate checks if request is valid
func (v Request) Validate() error {
	if v.Project == "" {
		return validation.NewErrBadRequestFieldValue("project", "missing project")
	}
	for i, c := range v.Commands {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("invalid command at %v: %w", i, err)
		}
	}
	return nil
}

// RequestCommand holds parameters for command to be executed
type RequestCommand struct {
	datatug.Credentials // holds username & password, if not provided trusted connection
	datatug.ServerRef
	Env        string              `json:"env"`
	DB         string              `json:"db"`
	Text       string              `json:"text"`
	Parameters []datatug.Parameter `json:"parameters"`
}

// Validate checks of command request is valid
func (v RequestCommand) Validate() error {
	if v.Env == "" {
		return validation.NewErrRequestIsMissingRequiredField("env")
	}
	if v.Text == "" {
		return validation.NewErrRequestIsMissingRequiredField("text")
	}
	if err := v.ServerRef.Validate(); err != nil {
		return err
	}
	if v.DB != "" {
		if v.Host != "" {
			return validation.NewBadRequestError(errors.New("both 'db' & 'host' were provided"))
		}
		if v.Driver != "" {
			return validation.NewBadRequestError(errors.New("both 'db' & 'driver' were provided"))
		}
		if v.Port != 0 {
			return validation.NewBadRequestError(errors.New("both 'db' & 'port' were provided"))
		}
	}
	return nil
}
