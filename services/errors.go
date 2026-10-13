package services

import (
	"fmt"

	"srcinsight/internal/model"
)

func errUnsupported(lang model.Language) error {
	return fmt.Errorf("services: unsupported language %q", string(lang))
}

func errParseFailed(lang model.Language) error {
	return fmt.Errorf("services: parse failed (%s)", string(lang))
}
