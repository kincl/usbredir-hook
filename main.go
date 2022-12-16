/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	vmSchema "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/log"

	"kubevirt.io/kubevirt/pkg/hooks"
	hooksInfo "kubevirt.io/kubevirt/pkg/hooks/info"
	hooksV1alpha1 "kubevirt.io/kubevirt/pkg/hooks/v1alpha1"
	hooksV1alpha2 "kubevirt.io/kubevirt/pkg/hooks/v1alpha2"

	"github.com/google/gousb"
	"github.com/google/gousb/usbid"
	"libvirt.org/go/libvirtxml"
)

const (
	vendorProductAnnotation      = "usbredir.vm.kubevirt.io/vendorProduct"
	onDefineDomainLoggingMessage = "Hook's OnDefineDomain callback method has been called"
)

type infoServer struct {
	Version string
}

func (s infoServer) Info(ctx context.Context, params *hooksInfo.InfoParams) (*hooksInfo.InfoResult, error) {
	log.Log.Info("Hook's Info method has been called")

	return &hooksInfo.InfoResult{
		Name: "smbios",
		Versions: []string{
			s.Version,
		},
		HookPoints: []*hooksInfo.HookPoint{
			{
				Name:     hooksInfo.OnDefineDomainHookPointName,
				Priority: 0,
			},
		},
	}, nil
}

type v1alpha1Server struct{}
type v1alpha2Server struct{}

func (s v1alpha2Server) OnDefineDomain(ctx context.Context, params *hooksV1alpha2.OnDefineDomainParams) (*hooksV1alpha2.OnDefineDomainResult, error) {
	log.Log.Info(onDefineDomainLoggingMessage)
	newDomainXML, err := onDefineDomain(params.GetVmi(), params.GetDomainXML())
	if err != nil {
		return nil, err
	}
	return &hooksV1alpha2.OnDefineDomainResult{
		DomainXML: newDomainXML,
	}, nil
}
func (s v1alpha2Server) PreCloudInitIso(_ context.Context, params *hooksV1alpha2.PreCloudInitIsoParams) (*hooksV1alpha2.PreCloudInitIsoResult, error) {
	return &hooksV1alpha2.PreCloudInitIsoResult{
		CloudInitData: params.GetCloudInitData(),
	}, nil
}

func (s v1alpha1Server) OnDefineDomain(ctx context.Context, params *hooksV1alpha1.OnDefineDomainParams) (*hooksV1alpha1.OnDefineDomainResult, error) {
	log.Log.Info(onDefineDomainLoggingMessage)
	newDomainXML, err := onDefineDomain(params.GetVmi(), params.GetDomainXML())
	if err != nil {
		return nil, err
	}
	return &hooksV1alpha1.OnDefineDomainResult{
		DomainXML: newDomainXML,
	}, nil
}

func onDefineDomain(vmiJSON []byte, domainXML []byte) ([]byte, error) {
	vmiSpec := vmSchema.VirtualMachineInstance{}
	err := json.Unmarshal(vmiJSON, &vmiSpec)
	if err != nil {
		log.Log.Reason(err).Errorf("Failed to unmarshal given VMI spec: %s", vmiJSON)
		panic(err)
	}

	annotations := vmiSpec.GetAnnotations()

	if _, found := annotations[vendorProductAnnotation]; !found {
		log.Log.Info("usbredir hook sidecar was requested, but no attributes provided. Returning original domain spec")
		return domainXML, nil
	}

	log.Log.Infof("got annotation %s: %s", vendorProductAnnotation, annotations[vendorProductAnnotation])

	vpid := strings.Split(annotations[vendorProductAnnotation], ":")
	vidu64, _ := strconv.ParseUint(vpid[0], 16, 16)
	vid := gousb.ID(uint16(vidu64))

	pidu64, _ := strconv.ParseUint(vpid[1], 16, 16)
	pid := gousb.ID(uint16(pidu64))

	/*
		...
		<devices>
		  <hostdev mode='subsystem' type='usb'>
		    <source startupPolicy='optional' guestReset='off'>
		      <vendor id='0x1234'/>
		      <product id='0xbeef'/>
		    </source>
		    <boot order='2'/>
		  </hostdev>
		</devices>
		...
	*/

	// domainSpec := libvirtxml.Domain{}
	// err = xml.Unmarshal(domainXML, &domainSpec)
	// if err != nil {
	// 	log.Log.Reason(err).Errorf("Failed to unmarshal given domain spec: %s", domainXML)
	// 	panic(err)
	// }

	// domainSpec := domainSchema.DomainSpec{}
	// err = xml.Unmarshal(domainXML, &domainSpec)
	// if err != nil {
	// 	log.Log.Reason(err).Errorf("Failed to unmarshal given domain spec: %s", domainXML)
	// 	panic(err)
	// }

	// hostDevice := domainSchema.HostDevice{
	// 	Mode: "subsystem",
	// 	Type: "usb",
	// 	// <source/> does not have <vendor/> or <product/> defined
	// 	Source: domainSchema.HostDeviceSource{
	// 		Address: &domainSchema.Address{},
	// 	},
	// }
	// domainSpec.Devices.HostDevices = append(domainSpec.Devices.HostDevices, hostDevice)

	domainSpec := &libvirtxml.Domain{}
	xml.Unmarshal([]byte(domainXML), &domainSpec)

	// Initialize a new Context.
	ctx := gousb.NewContext()
	defer ctx.Close()

	// Iterate through available Devices, finding all that match a known VID/PID.
	// vid, pid := gousb.ID(0x1050), gousb.ID(0x0407)
	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		// this function is called for every device present.
		// Returning true means the device should be opened.
		return desc.Vendor == vid && desc.Product == pid
	})
	// All returned devices are now open and will need to be closed.
	for _, d := range devs {
		defer d.Close()
	}
	if err != nil {
		log.Log.Errorf("OpenDevices(): %v", err)
		return domainXML, nil
	}
	if len(devs) == 0 {
		log.Log.Errorf("no devices found matching VID %s and PID %s", vid, pid)
		return domainXML, nil
	}

	// Pick the first device found.
	dev := devs[0]
	log.Log.Infof("found %03d.%03d %s:%s %s\n", dev.Desc.Bus, dev.Desc.Address, dev.Desc.Vendor, dev.Desc.Product, usbid.Describe(dev.Desc))

	bus := uint(dev.Desc.Bus)
	device := uint(dev.Desc.Address)

	usbDevice := libvirtxml.DomainHostdev{
		SubsysUSB: &libvirtxml.DomainHostdevSubsysUSB{
			Source: &libvirtxml.DomainHostdevSubsysUSBSource{
				Address: &libvirtxml.DomainAddressUSB{
					Bus:    &bus,
					Device: &device,
				},
			},
		},
	}

	hostdevs := domainSpec.Devices.Hostdevs
	hostdevs = append(hostdevs, usbDevice)
	domainSpec.Devices.Hostdevs = hostdevs

	newDomainXML, err := xml.Marshal(domainSpec)
	if err != nil {
		log.Log.Reason(err).Errorf("Failed to marshal updated domain spec: %+v", domainSpec)
		return domainXML, nil
	}

	log.Log.Info("Successfully updated original domain spec with requested attributes")
	return newDomainXML, nil
}

func main() {
	log.InitializeLogging("usbredir-hook")

	var version string
	pflag.StringVar(&version, "version", "", "hook version to use")
	pflag.Parse()

	socketPath := filepath.Join(hooks.HookSocketsSharedDirectory, "smbios.sock")
	socket, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Log.Reason(err).Errorf("Failed to initialized socket on path: %s", socket)
		log.Log.Error("Check whether given directory exists and socket name is not already taken by other file")
		panic(err)
	}
	defer os.Remove(socketPath)

	server := grpc.NewServer([]grpc.ServerOption{}...)

	if version == "" {
		panic(fmt.Errorf("usage: \n        /usbredir-hook --version v1alpha1|v1alpha2"))
	}
	hooksInfo.RegisterInfoServer(server, infoServer{Version: version})
	hooksV1alpha1.RegisterCallbacksServer(server, v1alpha1Server{})
	hooksV1alpha2.RegisterCallbacksServer(server, v1alpha2Server{})
	log.Log.Infof("Starting hook server exposing 'info' and 'v1alpha1' services on socket %s", socketPath)
	server.Serve(socket)
}
