package jsonrpc

func middlewareChain(middlewares []EndpointMiddleware) EndpointMiddleware {
	return func(next Endpoint) Endpoint {
		if len(middlewares) == 0 {
			return next
		}
		outer := middlewares[0]
		others := middlewares[1:]
		for i := len(others) - 1; i >= 0; i-- {
			next = others[i](next)
		}
		return outer(next)
	}
}
