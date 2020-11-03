package client

import (
	"github.com/leg100/stok/api/stok.goalspike.com/v1alpha1"
	sfake "github.com/leg100/stok/pkg/k8s/stokclient/fake"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/testing"
)

// Implements ClientCreator
type FakeClientCreator struct {
	// Fake objs
	objs                                 []runtime.Object
	kubeReactors, stokReactors           []testing.SimpleReactor
	kubeWatchReactors, stokWatchReactors []testing.SimpleWatchReactor
}

func NewFakeClientCreator(objs ...runtime.Object) ClientCreator {
	return &FakeClientCreator{objs: objs}
}

func (f *FakeClientCreator) Create(kubeCtx string) (*Client, error) {
	var kubeObjs, stokObjs []runtime.Object
	for _, obj := range f.objs {
		switch obj.(type) {
		case *v1alpha1.Run, *v1alpha1.Workspace:
			stokObjs = append(stokObjs, obj)
		default:
			kubeObjs = append(kubeObjs, obj)
		}
	}

	kubeClient := kfake.NewSimpleClientset(kubeObjs...)
	stokClient := sfake.NewSimpleClientset(stokObjs...)

	for _, r := range f.kubeReactors {
		kubeClient.PrependReactor(r.Verb, r.Resource, r.Reaction)
	}

	for _, r := range f.kubeWatchReactors {
		kubeClient.PrependWatchReactor(r.Resource, r.Reaction)
	}

	for _, r := range f.stokReactors {
		stokClient.PrependReactor(r.Verb, r.Resource, r.Reaction)
	}

	for _, r := range f.stokWatchReactors {
		stokClient.PrependWatchReactor(r.Resource, r.Reaction)
	}

	return &Client{
		Config:     &rest.Config{},
		KubeClient: kubeClient,
		StokClient: stokClient,
	}, nil
}

func (f *FakeClientCreator) PrependKubeReactor(verb, resource string, reaction testing.ReactionFunc) {
	f.kubeReactors = append(f.kubeReactors, testing.SimpleReactor{verb, resource, reaction})
}

func (f *FakeClientCreator) PrependKubeWatchReactor(resource string, reaction testing.WatchReactionFunc) {
	f.kubeWatchReactors = append(f.kubeWatchReactors, testing.SimpleWatchReactor{resource, reaction})
}

func (f *FakeClientCreator) PrependStokReactor(verb, resource string, reaction testing.ReactionFunc) {
	f.stokReactors = append(f.stokReactors, testing.SimpleReactor{verb, resource, reaction})
}

func (f *FakeClientCreator) PrependStokWatchReactor(verb, resource string, reaction testing.WatchReactionFunc) {
	f.stokWatchReactors = append(f.stokWatchReactors, testing.SimpleWatchReactor{resource, reaction})
}
