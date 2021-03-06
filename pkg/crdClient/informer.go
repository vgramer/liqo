package crdClient

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"reflect"
	"time"
)

// WatchResources is a wrapper cache function that allows to create either a real cache
// or a fake one, depending on the global variable Fake
func WatchResources(clientSet NamespacedCRDClientInterface,
	resource, namespace string,
	resyncPeriod time.Duration,
	handlers cache.ResourceEventHandlerFuncs,
	lo metav1.ListOptions) (cache.Store, chan struct{}, error) {

	if Fake {

		return WatchfakeResources(resource, handlers)
	} else {
		return WatchRealResources(clientSet, resource, namespace, resyncPeriod, handlers, lo)
	}
}

// Watch RealResources creates
func WatchRealResources(clientSet NamespacedCRDClientInterface,
	resource, namespace string,
	resyncPeriod time.Duration,
	handlers cache.ResourceEventHandlerFuncs,
	lo metav1.ListOptions) (cache.Store, chan struct{}, error) {

	listFunc := func(ls metav1.ListOptions) (result runtime.Object, err error) {
		ls = lo
		return clientSet.Resource(resource).Namespace(namespace).List(ls)
	}

	watchFunc := func(ls metav1.ListOptions) (watch.Interface, error) {
		ls = lo
		return clientSet.Resource(resource).Namespace(namespace).Watch(ls)
	}
	res, ok := Registry[resource]
	if !ok {
		return nil, nil, fmt.Errorf("reflection for api %v not set", resource)
	}
	t := reflect.New(res.SingularType).Interface().(runtime.Object)

	store, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc:  listFunc,
			WatchFunc: watchFunc,
		},
		t,
		resyncPeriod,
		handlers,
	)

	stopChan := make(chan struct{}, 1)

	go controller.Run(stopChan)

	return store, stopChan, nil
}

// WatchfakeResources creates a Fake custom informer, useful for testing purposes
// TODO: to implement all the caching functionality, such as resync, filtering, etc.
func WatchfakeResources(resource string, handlers cache.ResourceEventHandlerFuncs) (cache.Store, chan struct{}, error) {
	res, ok := Registry[resource]
	if !ok {
		return nil, nil, fmt.Errorf("reflection for api %v not set", resource)
	}

	store, stop := NewFakeCustomInformer(handlers, res.Keyer, res.Resource)
	return store, stop, nil
}
