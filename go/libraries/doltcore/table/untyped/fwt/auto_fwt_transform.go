package fwt

import (
	"github.com/attic-labs/noms/go/types"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/schema"
	"github.com/liquidata-inc/ld/dolt/go/libraries/doltcore/table/pipeline"
)

// AutoSizingFWTTransformer samples rows to automatically determine maximum column widths to provide to FWTTransformer.
type AutoSizingFWTTransformer struct {
	// The number of rows to sample to determine column widths
	numSamples  int
	// A map of column tag to max print width
	printWidths map[uint64]int
	// A map of column tag to max number of runes
	maxRunes    map[uint64]int
	// A buffer of rows to process
	rowBuffer   []pipeline.RowWithProps
	// The schema being examined
	sch         schema.Schema
	// The behavior to use for a value that's too long to print
	tooLngBhv   TooLongBehavior
	// The underlying fixed width transformer being assembled by row sampling.
	fwtTr       *FWTTransformer
}

func NewAutoSizingFWTTransformer(sch schema.Schema, tooLngBhv TooLongBehavior, numSamples int) *AutoSizingFWTTransformer {
	return &AutoSizingFWTTransformer{
		numSamples:  numSamples,
		printWidths: make(map[uint64]int, sch.GetAllCols().Size()),
		maxRunes:    make(map[uint64]int, sch.GetAllCols().Size()),
		rowBuffer:   make([]pipeline.RowWithProps, 0, 128),
		sch:         sch,
		tooLngBhv:   tooLngBhv,
	}
}

func (asTr *AutoSizingFWTTransformer) TransformToFWT(inChan <-chan pipeline.RowWithProps, outChan chan<- pipeline.RowWithProps, badRowChan chan<- *pipeline.TransformRowFailure, stopChan <-chan struct{}) {
RowLoop:
	for {
		select {
		case <-stopChan:
			return
		default:
		}

		select {
		case r, ok := <-inChan:
			if ok {
				asTr.handleRow(r, outChan, badRowChan, stopChan)
			} else {
				break RowLoop
			}
		case <-stopChan:
			return
		}
	}

	asTr.flush(outChan, badRowChan, stopChan)
}

func (asTr *AutoSizingFWTTransformer) handleRow(r pipeline.RowWithProps, outChan chan<- pipeline.RowWithProps, badRowChan chan<- *pipeline.TransformRowFailure, stopChan <-chan struct{}) {
	if asTr.rowBuffer == nil {
		asTr.processRow(r, outChan, badRowChan)
	} else if asTr.numSamples <= 0 || len(asTr.rowBuffer) < asTr.numSamples {
		r.Row.IterSchema(asTr.sch, func(tag uint64, val types.Value) (stop bool) {
			if !types.IsNull(val) {
				strVal := val.(types.String)
				printWidth := StringWidth(string(strVal))
				numRunes := len([]rune(string(strVal)))

				if printWidth > asTr.printWidths[tag] {
					asTr.printWidths[tag] = printWidth
				}
				if numRunes > asTr.maxRunes[tag] {
					asTr.maxRunes[tag] = numRunes
				}
			}
			return false
		})

		asTr.rowBuffer = append(asTr.rowBuffer, r)
	} else {
		asTr.flush(outChan, badRowChan, stopChan)
	}
}

func (asWr *AutoSizingFWTTransformer) flush(outChan chan<- pipeline.RowWithProps, badRowChan chan<- *pipeline.TransformRowFailure, stopChan <-chan struct{}) {
	if asWr.fwtTr == nil {
		fwtSch := NewFWTSchemaWithWidths(asWr.sch, asWr.printWidths, asWr.maxRunes)
		asWr.fwtTr = NewFWTTransformer(fwtSch, asWr.tooLngBhv)
	}

	for i := 0; i < len(asWr.rowBuffer); i++ {
		asWr.processRow(asWr.rowBuffer[i], outChan, badRowChan)

		if i%100 == 0 {
			select {
			case <-stopChan:
				return
			default:
			}
		}
	}

	asWr.rowBuffer = nil
}

func (asTr *AutoSizingFWTTransformer) processRow(rowWithProps pipeline.RowWithProps, outChan chan<- pipeline.RowWithProps, badRowChan chan<- *pipeline.TransformRowFailure) {
	rds, errMsg := asTr.fwtTr.Transform(rowWithProps.Row, rowWithProps.Props)

	if errMsg != "" {
		badRowChan <- &pipeline.TransformRowFailure{
			Row:           rowWithProps.Row,
			TransformName: "Auto Sizing Fixed Width Transform",
			Details:       errMsg,
		}
	} else if len(rds) == 1 {
		propUpdates := rds[0].PropertyUpdates

		outProps := rowWithProps.Props
		if len(propUpdates) > 0 {
			outProps = outProps.Set(propUpdates)
		}

		outRow := pipeline.RowWithProps{rds[0].RowData, outProps}
		outChan <- outRow
	}
}
