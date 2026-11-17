// The forced-order state writer of nats-server v2.10.29, which server.patch calls in place of the
// server's own. packages/envoy/scripts/forced-order.sh adds it to the server package beside the
// shared ../zz_measure.go.
package server

import (
	"io/fs"
	"os"
)

// measureWriteFile is v2.10.29's writeFileWithOptionalSync: writeFileWithSync under SyncAlways,
// otherwise os.WriteFile under the dios semaphore. Both write the final path directly
// (O_WRONLY|O_CREATE|O_TRUNC), so this server has no temporary file and no rename, and no
// write.preRename point. Unchanged except for the measure calls between the steps.
func (o *consumerFileStore) measureWriteFile(name string, data []byte, perm fs.FileMode) error {
	<-dios
	defer func() {
		dios <- struct{}{}
	}()
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if o.fs.fcfg.SyncAlways {
		flags |= os.O_SYNC
	}
	f, err := os.OpenFile(name, flags, perm)
	if err != nil {
		measure(o.mev("write.openErr", data, 0, err))
		return err
	}
	_, err = f.Write(data)
	if err1 := f.Close(); err1 != nil && err == nil {
		err = err1
	}
	measure(o.mev("write.written", data, 0, err))
	return err
}
