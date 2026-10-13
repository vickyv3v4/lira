/**
* @Author: wei.peng03
* @Date: 2022/7/20
 */

package xcontext

type Service string

func (s Service) String() string {
	return string(s)
}

const (
	Demo                    Service = "demo"
	CloudGatewayAdmin       Service = "gcloudgwadmin"
	CloudGatewayServer      Service = "gcloudgwserver"
	CloudGatewayServerB     Service = "gc-bgw-server"
	CloudVerifyCenter       Service = "gcloudverify"
	CloudEcsPortal          Service = "gcloudecs-p"
	CloudEcsResource        Service = "gcloudecs-r"
	CloudVpcPortal          Service = "gcloudvpc-p"
	CloudVpcResource        Service = "gcloudvpc-r"
	CloudBareMetalPortal    Service = "gcloudbm-p"
	CloudBareMetalResource  Service = "gcloudbm-r"
	CloudStoragePortal      Service = "gcloudstorage-p"
	CloudStorageResource    Service = "gcloudstorage-r"
	CloudContainerPortal    Service = "gcloudcontainer-p"
	CloudContainerResource  Service = "gcloudcontainer-r"
	CloudMiddlewarePortal   Service = "gcloudmw-p"
	CloudMiddlewareResource Service = "gcloudmw-r"
	CloudRam                Service = "gcloudram"
	CloudTenant             Service = "gcloudtenant"
	CloudEventCenter        Service = "gcloudevent"
	CloudResource           Service = "gcloudresource"
	CloudAudit              Service = "gcloudaudit"
	CloudBilling            Service = "gcloudbilling"
	CloudComputePool        Service = "gcloudcpool"
	CloudCboot              Service = "gc-cboot-plt"
	CloudBdcResource        Service = "gc-bdc-res"
	CloudRos                Service = "gc-ros-plt"
	CloudRosServer          Service = "gc-ros-server"
	CloudEventServer        Service = "gc-event-server"
	CloudTenantSS           Service = "gc-tenant-ss"
)
