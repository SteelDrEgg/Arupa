package spec

// CloneServiceRecord returns a deep-enough copy for publication and concurrent
// readers.
func CloneServiceRecord(record *ServiceRecord) *ServiceRecord {
	if record == nil {
		return nil
	}
	out := *record
	out.Transports = make([]Transport, len(record.Transports))
	for index, declaration := range record.Transports {
		out.Transports[index] = declaration
		if declaration.Proxy != nil {
			proxy := *declaration.Proxy
			out.Transports[index].Proxy = &proxy
		}
	}
	out.Routes = make([]Route, len(record.Routes))
	for index, declaration := range record.Routes {
		out.Routes[index] = declaration
		if declaration.HTTP != nil {
			httpRoute := *declaration.HTTP
			httpRoute.Access.Groups = append([]string(nil), declaration.HTTP.Access.Groups...)
			out.Routes[index].HTTP = &httpRoute
		}
		if declaration.SocketIO != nil {
			socketRoute := *declaration.SocketIO
			socketRoute.Events = append([]string(nil), declaration.SocketIO.Events...)
			socketRoute.Access.Groups = append([]string(nil), declaration.SocketIO.Access.Groups...)
			if len(declaration.SocketIO.EventAccess) > 0 {
				socketRoute.EventAccess = make(map[string]AccessPolicy, len(declaration.SocketIO.EventAccess))
				for event, policy := range declaration.SocketIO.EventAccess {
					policy.Groups = append([]string(nil), policy.Groups...)
					socketRoute.EventAccess[event] = policy
				}
			}
			out.Routes[index].SocketIO = &socketRoute
		}
	}
	return &out
}
