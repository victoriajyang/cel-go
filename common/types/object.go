// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types

import (
	"fmt"
	"reflect"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/google/cel-go/common/types/pb"
	"github.com/google/cel-go/common/types/ref"
)

type protoObj struct {
	ref.TypeAdapter
	value     proto.Message
	refValue  reflect.Value
	typeDesc  *pb.TypeDescription
	typeValue *TypeValue
	isAny     bool
}

// NewObject returns an object based on a proto.Message value which handles
// conversion between protobuf type values and expression type values.
// Objects support indexing and iteration.
// Note:  only uses default Db.
func NewObject(adapter ref.TypeAdapter,
	typeDesc *pb.TypeDescription,
	value proto.Message) ref.Val {
	return &protoObj{
		TypeAdapter: adapter,
		value:       value,
		refValue:    reflect.ValueOf(value),
		typeDesc:    typeDesc,
		typeValue:   NewObjectTypeValue(typeDesc.Name())}
}

func (o *protoObj) ConvertToNative(refl reflect.Type) (interface{}, error) {
	if refl.AssignableTo(o.refValue.Type()) {
		return o.value, nil
	}
	if refl == anyValueType {
		return ptypes.MarshalAny(o.Value().(proto.Message))
	}
	// If the object is already assignable to the desired type return it.
	if reflect.TypeOf(o).AssignableTo(refl) {
		return o, nil
	}
	return nil, fmt.Errorf("type conversion error from '%v' to '%v'",
		o.refValue.Type(), refl)
}

func (o *protoObj) ConvertToType(typeVal ref.Type) ref.Val {
	switch typeVal {
	default:
		if o.Type().TypeName() == typeVal.TypeName() {
			return o
		}
	case TypeType:
		return o.typeValue
	}
	return NewErr("type conversion error from '%s' to '%s'",
		o.typeDesc.Name(), typeVal)
}

func (o *protoObj) Equal(other ref.Val) ref.Val {
	if o.typeDesc.Name() != other.Type().TypeName() {
		return ValOrErr(other, "no such overload")
	}
	return Bool(proto.Equal(o.value, other.Value().(proto.Message)))
}

// IsSet tests whether a field which is defined is set to a non-default value.
func (o *protoObj) IsSet(field ref.Val) ref.Val {
	protoFieldName, ok := field.(String)
	if !ok {
		return ValOrErr(field, "no such overload")
	}
	protoFieldStr := string(protoFieldName)
	f, found := o.typeDesc.FieldByName(protoFieldStr)
	if !found {
		return NewErr("no such field '%s'", field)
	}
	if !f.SupportsPresence() {
		return NewErr("field does not support presence testing.")
	}
	getter := o.refValue.MethodByName(f.GetterName())
	if !getter.IsValid() {
		return NewErr("no such field '%s'", field)
	}
	refField := getter.Call([]reflect.Value{})[0]
	if !refField.IsValid() {
		return NewErr("no such field '%s'", field)
	}
	return isFieldSet(refField)
}

func (o *protoObj) Get(index ref.Val) ref.Val {
	protoFieldName, ok := index.(String)
	if !ok {
		return ValOrErr(index, "no such overload")
	}
	protoFieldStr := string(protoFieldName)
	fd, found := o.typeDesc.FieldByName(protoFieldStr)
	if !found {
		return NewErr("no such field '%s'", index)
	}
	getter := o.refValue.MethodByName(fd.GetterName())
	if !getter.IsValid() {
		return NewErr("no such field '%s'", index)
	}
	refField := getter.Call([]reflect.Value{})[0]
	if !refField.IsValid() {
		return NewErr("no such field '%s'", index)
	}
	return getOrDefaultInstance(o.TypeAdapter, fd, refField)
}

func (o *protoObj) Type() ref.Type {
	return o.typeValue
}

func (o *protoObj) Value() interface{} {
	return o.value
}

func isFieldSet(refVal reflect.Value) ref.Val {
	if refVal.Kind() == reflect.Ptr && refVal.IsNil() {
		return False
	}
	return True
}

func getOrDefaultInstance(adapter ref.TypeAdapter,
	fd *pb.FieldDescription,
	refVal reflect.Value) ref.Val {
	if isFieldSet(refVal) == True {
		value := refVal.Interface()
		return adapter.NativeToValue(value)
	}
	if fd.IsWrapper() {
		return NullValue
	}
	if fd.IsMessage() {
		return adapter.NativeToValue(fd.Type().DefaultValue())
	}
	return NewErr("no default value for field: %s", fd.Name())
}
