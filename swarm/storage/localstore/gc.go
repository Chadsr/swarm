// Copyright 2018 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package localstore

import (
	"sync/atomic"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/swarm/shed"
	"github.com/syndtr/goleveldb/leveldb"
)

var (
	// gcTargetRatio defines the target number of items
	// in garbage collection index that will not be removed
	// on garbage collection. The target number of items
	// is calculated by gcTarget function. This value must be
	// in range (0,1]. For example, with 0.9 value,
	// garbage collection will leave 90% of defined capacity
	// in database after its run. This prevents frequent
	// garbage collection runt.
	gcTargetRatio = 0.9
	// gcBatchSize limits the number of chunks in a single
	// leveldb batch on garbage collection.
	gcBatchSize int64 = 1000
)

// collectGarbage is a long running function that waits for
// collectGarbageTrigger channel to signal a garbage collection
// run. GC run iterates on gcIndex and removes older items
// form retrieval and other indexes.
func (db *DB) collectGarbage() {
	target := db.gcTarget()
	for {
		select {
		case <-db.collectGarbageTrigger:
			batch := new(leveldb.Batch)

			// sets a gc trigger if batch limit is reached
			var triggerNextIteration bool
			var collectedCount int64
			err := db.gcIndex.IterateAll(func(item shed.IndexItem) (stop bool, err error) {
				gcSize := atomic.LoadInt64(&db.gcSize)
				if gcSize-collectedCount <= target {
					return true, nil
				}
				// delete from retrieve, pull, gc
				if db.useRetrievalCompositeIndex {
					db.retrievalCompositeIndex.DeleteInBatch(batch, item)
				} else {
					db.retrievalDataIndex.DeleteInBatch(batch, item)
					db.retrievalAccessIndex.DeleteInBatch(batch, item)
				}
				db.pullIndex.DeleteInBatch(batch, item)
				db.gcIndex.DeleteInBatch(batch, item)
				collectedCount++
				if collectedCount >= gcBatchSize {
					triggerNextIteration = true
					return true, nil
				}
				return false, nil
			})
			if err != nil {
				log.Error("localstore collect garbage", "err", err)
			}

			err = db.shed.WriteBatch(batch)
			if err != nil {
				log.Error("localstore collect garbage write batch", "err", err)
			} else {
				// batch is written, decrement gcSize and check if another gc run is needed
				db.incGCSize(-collectedCount)
				if triggerNextIteration {
					select {
					case db.collectGarbageTrigger <- struct{}{}:
					default:
					}
				}
			}

			if testHookCollectGarbage != nil {
				testHookCollectGarbage(collectedCount)
			}
		case <-db.close:
			return
		}
	}
}

// gcTrigger retruns the absolute value for garbage collection
// target value, calculated from db.capacity and gcTargetRatio.
func (db *DB) gcTarget() (target int64) {
	return int64(float64(db.capacity) * gcTargetRatio)
}

// incGCSize increments gcSize by the provided number.
// If count is negative, it will decrement gcSize.
func (db *DB) incGCSize(count int64) {
	new := atomic.AddInt64(&db.gcSize, count)
	if new >= db.capacity {
		select {
		case db.collectGarbageTrigger <- struct{}{}:
		default:
		}
	}
}

// testHookCollectGarbage is a hook that can provide
// information when a garbage collection run is done
// and how many items it removed.
var testHookCollectGarbage func(collectedCount int64)