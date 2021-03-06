package crdClient

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"reflect"
)

type KeyerFunc func(obj runtime.Object) (string, error)

type RegistryType struct {
	SingularType reflect.Type
	PluralType   reflect.Type

	Keyer    KeyerFunc
	Resource schema.GroupResource
}

var Registry = make(map[string]RegistryType)

func AddToRegistry(api string, singular, plural runtime.Object, keyer KeyerFunc, resource schema.GroupResource) {
	Registry[api] = RegistryType{
		SingularType: reflect.TypeOf(singular).Elem(),
		PluralType:   reflect.TypeOf(plural).Elem(),
		Keyer:        keyer,
		Resource:     resource,
	}
}
