package helmrender

import (
	"bufio"
	"io"

	"github.com/go-logr/logr"
)


func NewLogWriter(logger logr.Logger, keysAndValues ...interface{}) io.Writer {
	reader, writer := io.Pipe()
	scanner := bufio.NewScanner(reader)
	go func ()  {
		for tok := scanner.Scan(); tok; tok = scanner.Scan() {
			logger.Info(scanner.Text(), keysAndValues)
		}
	}()
	return writer
}