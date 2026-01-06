package dtconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/strongo/validation"
)

// ProjectRef hold project configuration, specifically path to project directory
type ProjectRef struct {
	ID     string `yaml:"id"`
	Path   string `yaml:"path,omitempty"`   // Local path
	Origin string `yaml:"origin,omitempty"` // Format: github.com/{owner}/{repo}
	Title  string `yaml:"title"`
}

func (v ProjectRef) Validate() error {
	var empty ProjectRef
	if v == empty {
		return errors.New("project ref is empty")
	}
	if v.ID == "" {
		return validation.NewErrRecordIsMissingRequiredField("id")
	}
	if v.Path == "" && v.Origin == "" {
		return fmt.Errorf("at least one of this fields must be set: %w",
			validation.NewErrRecordIsMissingRequiredField("path|origin"))
	}
	if strings.TrimSpace(v.Title) == "" {
		return validation.NewErrRecordIsMissingRequiredField("title")
	}
	return nil
}
