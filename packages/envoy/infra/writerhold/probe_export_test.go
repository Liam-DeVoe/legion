package writerhold_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sjawhar/envoy/infra/writerhold"
)

// No caller outside writerhold can read a refusal of the probe without Operator.Probe's check: no
// exported field of Operator or Restart is a dial of the probe (a func of a context and a URL, with
// or without a token, answering an error), so a caller can neither build a Restart whose proof
// dials its own probe nor dial the probe's raw connect and count its refusal. The probe is reached
// only through the Probe method, and a Restart through RestartProveAndBarrier (round 4's Minor 2).
// Control: 0d121d82b8f, which exported Restart.Probe and Operator.ProbeDial.
func TestNoExportedFieldDialsTheProbeOutsideItsCheck(t *testing.T) {
	dials := []reflect.Type{
		reflect.TypeFor[func(context.Context, string) error](),
		reflect.TypeFor[func(context.Context, string, string) error](),
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[writerhold.Operator](), reflect.TypeFor[writerhold.Restart]()} {
		for field := range typ.Fields() {
			for _, dial := range dials {
				if field.IsExported() && field.Type == dial {
					t.Errorf("%s.%s is an exported dial of the probe (%s): a caller outside writerhold reads its refusal without Probe's check", typ.Name(), field.Name, field.Type)
				}
			}
		}
	}
}
