package store

import (
	"fmt"
	"reflect"

	"github.com/stone7890/leash/internal/domain/money"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The BSON codec for money lives HERE, not in the money package.
//
// internal/domain imports nothing outside the standard library, and a BSON codec would drag the
// driver into the one package that must stay free of it. So the type is defined in the domain and
// taught to the driver here — which is also the only place that legitimately knows what BSON is.
//
// The guarantee itself is not this codec. It is `bsonType: "long"` in every validator, which holds
// regardless of driver version, shell session, or which of the two runtimes wrote the document.
// This codec is the ergonomic half; the schema is the enforcing half.

var moneyType = reflect.TypeOf(money.Base(0))

type moneyCodec struct{}

// EncodeValue always writes int64. Never a double, never an int32, and never a decimal.
func (moneyCodec) EncodeValue(_ bson.EncodeContext, w bson.ValueWriter, val reflect.Value) error {
	if !val.IsValid() || val.Type() != moneyType {
		return bson.ValueEncoderError{Name: "moneyCodec", Types: []reflect.Type{moneyType}, Received: val}
	}
	return w.WriteInt64(val.Int())
}

// DecodeValue is LOUD rather than lenient.
//
// An int32 in the database means something wrote a raw document and bypassed the schema. A double
// means money has already been lost. Neither is a value to coerce into something plausible — both
// are an incident, and quietly rounding one would destroy the evidence that it happened.
func (moneyCodec) DecodeValue(_ bson.DecodeContext, r bson.ValueReader, val reflect.Value) error {
	if !val.CanSet() || val.Type() != moneyType {
		return bson.ValueDecoderError{Name: "moneyCodec", Types: []reflect.Type{moneyType}, Received: val}
	}
	switch r.Type() {
	case bson.TypeInt64:
		v, err := r.ReadInt64()
		if err != nil {
			return err
		}
		val.SetInt(v)
		return nil
	case bson.TypeInt32:
		return fmt.Errorf("%w: an amount is stored as int32, which means a write bypassed the "+
			"schema — investigate before reading further", money.ErrStoredAsInt32)
	case bson.TypeDouble:
		return fmt.Errorf("%w: an amount is stored as a double, which means money has already "+
			"been lost to rounding — do not coerce it", money.ErrStoredAsDouble)
	case bson.TypeDecimal128:
		return fmt.Errorf("%w: an amount is stored as decimal128", money.ErrStoredAsDecimal)
	case bson.TypeNull, bson.TypeUndefined:
		if err := r.Skip(); err != nil {
			return err
		}
		val.SetInt(0)
		return nil
	default:
		return fmt.Errorf("an amount is stored as %s, which is not a number", r.Type())
	}
}

// registry teaches the driver about money.Base. Every client in this package is built with it, so
// there is no path that encodes an amount without going through the codec above.
func registry() *bson.Registry {
	r := bson.NewRegistry()
	r.RegisterTypeEncoder(moneyType, moneyCodec{})
	r.RegisterTypeDecoder(moneyType, moneyCodec{})
	return r
}
