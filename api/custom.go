package api

import (
	"fmt"
	htype "github.com/banzaicloud/banzai-types/components/helm"
	"github.com/banzaicloud/pipeline/helm"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"strings"
)

// ListEndpoints lists service public endpoints
func ListEndpoints(c *gin.Context) {
	log := logger.WithFields(logrus.Fields{"tag": "ListEndpoints"})

	kubeConfig, ok := GetK8sConfig(c)
	if ok != true {
		return
	}

	client, err := helm.GetK8sConnection(kubeConfig)
	if err != nil {
		log.Errorf("Error getting k8s connection: %s", err.Error())
		c.JSON(http.StatusBadRequest, htype.ErrorResponse{
			Code:    http.StatusBadRequest,
			Message: "Error getting k8s connection",
			Error:   err.Error(),
		})
		return
	}

	serviceList, err := client.CoreV1().Services("").List(meta_v1.ListOptions{})
	if err != nil {
		log.Errorf("Error listing services: %s", err.Error())
		c.JSON(http.StatusNotFound, htype.ErrorResponse{
			Code:    http.StatusNotFound,
			Message: "Error during listing services",
			Error:   err.Error(),
		})
		return
	}

	ingressList, err := client.ExtensionsV1beta1().Ingresses("").List(meta_v1.ListOptions{})
	if err != nil {
		log.Errorf("Error listing ingresses: %s", err)
		c.JSON(http.StatusInternalServerError, htype.ErrorResponse{
			Code:    http.StatusInternalServerError,
			Message: fmt.Sprintf("List kubernetes ingresses failed: %+v", err),
		})
		return
	}
	endpointList := getLoadBalancersWithIngressPaths(serviceList, ingressList)

	c.JSON(http.StatusOK, htype.EndpointResponse{
		Endpoints: endpointList,
	})
}

func getLoadBalancersWithIngressPaths(serviceList *v1.ServiceList, ingressList *v1beta1.IngressList) []*htype.EndpointItem {
	var endpointList []*htype.EndpointItem
	const traefik = "traefik"

	for _, service := range serviceList.Items {
		var endpointURLs []*htype.EndPointURLs
		log.Debugf("Service: %#v", service.Status)
		if len(service.Status.LoadBalancer.Ingress) > 0 {
			//TODO we should avoid differences on kubernetes level
			var publicIP string
			if service.Status.LoadBalancer.Ingress[0].Hostname != "" {
				publicIP = service.Status.LoadBalancer.Ingress[0].Hostname
			} else {
				publicIP = service.Status.LoadBalancer.Ingress[0].IP
			}
			ports := make(map[string]int32)
			if len(service.Spec.Ports) > 0 {
				for _, port := range service.Spec.Ports {
					ports[port.Name] = port.Port
				}
			}
			if strings.Contains(service.Spec.Selector["app"], traefik) {
				for _, ingress := range ingressList.Items {
					log.Debugf("Inspecting ingress: %s", ingress.Name)
					if ingress.Annotations["kubernetes.io/ingress.class"] == traefik {
						endpoints := getIngressEndpoints(publicIP, &ingress, serviceList)
						for i := 0; i < len(endpoints); i++ {
							endpointURLs = append(endpointURLs, &(endpoints[i]))
						}
					}
				}
			}
			endpointList = append(endpointList, &htype.EndpointItem{
				Name:         service.Name,
				Host:         publicIP,
				Ports:        ports,
				EndPointURLs: endpointURLs,
			})
		}
	}
	return endpointList
}

// getIngressEndpoints iterates through all the rules->paths defined in the given Ingress object
// and returns a collection of EndPointURLs form it.
// The EndPointURLs struct is constructed as:
//             EndPointURLs {
//                     ServiceName: {path from ingress rule}
//                     URL: http://{loadBalancerPublicHost}/{path from ingress rule}
//                     HelmReleaseName: {helm generated release name}
//             }
func getIngressEndpoints(loadBalancerPublicHost string, ingress *v1beta1.Ingress, serviceList *v1.ServiceList) []htype.EndPointURLs {
	var endpointUrls []htype.EndPointURLs

	for _, ingressRule := range ingress.Spec.Rules {
		for _, ingressPath := range ingressRule.HTTP.Paths {
			path := ingressPath.Path

			if !strings.HasSuffix(path, "/") {
				path += "/"
			}
			endpointUrls = append(endpointUrls,
				htype.EndPointURLs{
					ServiceName:     strings.Trim(path, "/"),
					URL:             fmt.Sprint("http://", loadBalancerPublicHost, path),
					HelmReleaseName: getIngressReleaseName(ingressPath.Backend, serviceList),
				})
		}
	}

	return endpointUrls
}

// getIngressReleaseName returns the release name generated by the helm for the specific ingress
func getIngressReleaseName(backend v1beta1.IngressBackend, serviceList *v1.ServiceList) string {
	serviceName := backend.ServiceName
	for _, service := range serviceList.Items {
		if service.Name == serviceName {
			return service.Labels["release"]
		}
	}
	return "No release name for this ingress."
}
