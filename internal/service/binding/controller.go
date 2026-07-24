// Package binding coordinates owner-scoped transport and route registration.
//
// This is the only component allowed to perform operations spanning both
// registries. Individual registries remain independent and have no back
// references to each other.
package binding

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"arupa/internal/service/route"
	"arupa/internal/service/spec"
	"arupa/internal/service/transport"
)

var ErrTransportInUse = errors.New("transport is still referenced by a route")

type ownerSession struct {
	owner     spec.BindingOwner
	root      string
	inherited map[string]string
}

type Publisher func(spec.BindingOwner)

// Controller is the service binding control plane.
type Controller struct {
	mu         sync.Mutex
	sessions   map[string]ownerSession
	transports *transport.Registry
	routes     *route.Registry
	publish    Publisher
}

func NewController(transports *transport.Registry, routes *route.Registry, publish Publisher) *Controller {
	return &Controller{
		sessions:   make(map[string]ownerSession),
		transports: transports,
		routes:     routes,
		publish:    publish,
	}
}

func (c *Controller) Attach(owner string, state spec.BindingOwner, root string, inherited map[string]string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("service owner is required")
	}
	if state == nil {
		return fmt.Errorf("service owner state is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.sessions[owner]; exists {
		return fmt.Errorf("service %q already has an active binding session", owner)
	}
	c.sessions[owner] = ownerSession{
		owner: state, root: root, inherited: transport.CloneStrings(inherited),
	}
	return nil
}

// Detach explicitly releases routes before their transports and then removes
// the owner session. Individual transport unregister never cascades to routes.
func (c *Controller) Detach(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routes.RemoveOwner(owner)
	c.transports.RemoveOwner(owner)
	delete(c.sessions, owner)
}

func (c *Controller) RegisterTransport(owner string, declaration spec.Transport) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[owner]
	if !ok {
		return fmt.Errorf("service %q has no active session", owner)
	}
	if err := c.transports.Register(owner, declaration, transport.Session{
		Endpoint: session.owner, Root: session.root, Inherited: session.inherited,
	}); err != nil {
		return err
	}
	session.owner.AddTransport(declaration)
	c.publishOwner(session.owner)
	return nil
}

func (c *Controller) UnregisterTransport(owner, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[owner]
	if !ok {
		return fmt.Errorf("service %q has no active session", owner)
	}
	if c.routes.UsesTransport(owner, strings.TrimSpace(id)) {
		return fmt.Errorf("%w: %q", ErrTransportInUse, strings.TrimSpace(id))
	}
	if err := c.transports.Unregister(owner, id); err != nil {
		return err
	}
	session.owner.RemoveTransport(strings.TrimSpace(id))
	c.publishOwner(session.owner)
	return nil
}

func (c *Controller) RegisterRoutes(owner string, declarations []spec.Route) spec.RegistrationResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[owner]
	if !ok {
		return spec.RegistrationResult{
			Degraded: true,
			Error:    fmt.Sprintf("service %q has no active session", owner),
		}
	}
	result := spec.RegistrationResult{}
	for _, declaration := range declarations {
		if err := c.routes.Register(owner, declaration); err != nil {
			result.Failures = append(result.Failures, spec.RegistrationFailure{
				ID: declaration.ID, Error: err.Error(),
			})
			result.Degraded = true
			continue
		}
		result.Registered = append(result.Registered, declaration.ID)
		session.owner.AddRoute(declaration)
	}
	if result.Degraded {
		session.owner.MarkDegraded()
	}
	c.publishOwner(session.owner)
	return result
}

func (c *Controller) UnregisterRoutes(owner string, ids []string) spec.RegistrationResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[owner]
	if !ok {
		return spec.RegistrationResult{Error: fmt.Sprintf("service %q has no active session", owner)}
	}
	result := spec.RegistrationResult{}
	for _, id := range ids {
		if err := c.routes.Unregister(owner, id); err != nil {
			result.Failures = append(result.Failures, spec.RegistrationFailure{
				ID: id, Error: err.Error(),
			})
			continue
		}
		result.Registered = append(result.Registered, id)
		session.owner.RemoveRoute(strings.TrimSpace(id))
	}
	c.publishOwner(session.owner)
	return result
}

func (c *Controller) publishOwner(owner spec.BindingOwner) {
	if c.publish != nil && owner != nil {
		c.publish(owner)
	}
}
