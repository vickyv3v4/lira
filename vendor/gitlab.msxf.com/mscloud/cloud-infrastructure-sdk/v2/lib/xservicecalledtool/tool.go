package xservicecalledtool

type ServiceRegistrar interface {
	Refresh()
}

type ServiceValidator interface {
	UpdateConf(conf ServiceAuth)
}

type CalledManager struct {
	Registrar           ServiceRegistrar
	ServiceValidator    ServiceValidator
	ServiceCallAuthConf ServiceAuth
	ServiceToken        string
	ServiceName         string
	CalledConf          *ServiceCalledConf
	FallbackF           FallbackFuc
}

func (c *CalledManager) SetServiceAndToken(service, token string) {
	c.ServiceName = service
	c.ServiceToken = token
}

func (c *CalledManager) SetFallbackFuc(f FallbackFuc) {
	c.FallbackF = f
}

var Manager = new(CalledManager)
