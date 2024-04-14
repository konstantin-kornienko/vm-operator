package build

import (
	victoriametricsv1beta1 "github.com/VictoriaMetrics/operator/api/v1beta1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VMServiceScrapeForServiceWithSpec build VMServiceScrape for given service with optional spec
// optionally could filter out ports from service
func VMServiceScrapeForServiceWithSpec(service *v1.Service, serviceScrapeSpec *victoriametricsv1beta1.VMServiceScrapeSpec, metricPath string, filterPortNames ...string) *victoriametricsv1beta1.VMServiceScrape {
	endPoints := []victoriametricsv1beta1.Endpoint{}
	for _, servicePort := range service.Spec.Ports {
		var nameMatched bool
		for _, filter := range filterPortNames {
			if servicePort.Name == filter {
				nameMatched = true
				break
			}
		}
		if len(filterPortNames) > 0 && !nameMatched {
			continue
		}

		endPoints = append(endPoints, victoriametricsv1beta1.Endpoint{
			Port: servicePort.Name,
			Path: metricPath,
		})
	}

	if serviceScrapeSpec == nil {
		serviceScrapeSpec = &victoriametricsv1beta1.VMServiceScrapeSpec{}
	}
	scrapeSvc := &victoriametricsv1beta1.VMServiceScrape{
		ObjectMeta: metav1.ObjectMeta{
			Name:            service.Name,
			Namespace:       service.Namespace,
			OwnerReferences: service.OwnerReferences,
			Labels:          service.Labels,
			Annotations:     service.Annotations,
		},
		Spec: *serviceScrapeSpec,
	}
	// merge generated endpoints into user defined values by Port name
	// assume, that it must be unique.
	for _, generatedEP := range endPoints {
		var found bool
		for idx := range scrapeSvc.Spec.Endpoints {
			eps := &scrapeSvc.Spec.Endpoints[idx]
			if eps.Port == generatedEP.Port {
				found = true
				if eps.Path == "" {
					eps.Path = generatedEP.Path
				}
			}
		}
		if !found {
			scrapeSvc.Spec.Endpoints = append(scrapeSvc.Spec.Endpoints, generatedEP)
		}
	}
	// allow to manually define selectors
	// in some cases it may be useful
	// for instance when additional service created with extra pod ports
	if scrapeSvc.Spec.Selector.MatchLabels == nil && scrapeSvc.Spec.Selector.MatchExpressions == nil {
		scrapeSvc.Spec.Selector = metav1.LabelSelector{MatchLabels: service.Spec.Selector, MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: victoriametricsv1beta1.AdditionalServiceLabel, Operator: metav1.LabelSelectorOpDoesNotExist},
		}}
	}

	return scrapeSvc
}
