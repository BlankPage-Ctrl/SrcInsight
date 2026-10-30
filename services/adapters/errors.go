package adapters

import (
	"fmt"

	"srcinsight/internal/model"
)

func errParseFailed(lang model.Language) error {
	return fmt.Errorf("services: parse failed (%s)", string(lang))
}
