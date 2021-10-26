package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v4/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v4/go/aws/ec2"
	"github.com/pulumi/pulumi-rancher2/sdk/v3/go/rancher2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"strconv"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {

		// Extract config items
		conf := config.New(ctx, "")
		ec2AccessKey := conf.GetSecret("rancherEC2AccessKey")
		ec2SecretKey := conf.GetSecret("rancherEC2SecretKey")
		createDownstreamCluster := conf.GetBool("installDownstreamCluster")
		installFleetClusters := conf.GetBool("installFleetClusters")
		downstreamClusterEC2Size := conf.Get("downstreamClusterEC2Size")
		fleetClustersEC2Size := conf.Get("fleetClustersEC2Size")

		cloudcredential, err := rancher2.NewCloudCredential(ctx, "david-pulumi-cloudcredential", &rancher2.CloudCredentialArgs{
			Name:        pulumi.String("david-pulumi-aws"),
			Description: pulumi.String("AWS credentials"),
			Amazonec2CredentialConfig: &rancher2.CloudCredentialAmazonec2CredentialConfigArgs{
				AccessKey: ec2AccessKey,
				SecretKey: ec2SecretKey,
			}})

		if err != nil {
			return err
		}

		// Create AWS VPC
		vpc, err := ec2.NewVpc(ctx, "david-pulumi-vpc", &ec2.VpcArgs{
			CidrBlock:          pulumi.String("10.0.0.0/16"),
			Tags:               pulumi.StringMap{"Name": pulumi.String("david-pulumi-vpc")},
			EnableDnsHostnames: pulumi.Bool(true),
			EnableDnsSupport:   pulumi.Bool(true),
		})

		if err != nil {
			return err
		}

		// Create IGW
		igw, err := ec2.NewInternetGateway(ctx, "david-pulumi-gw", &ec2.InternetGatewayArgs{
			VpcId: vpc.ID(),
		})

		if err != nil {
			return err
		}

		// Create AWS security group
		sg, err := ec2.NewSecurityGroup(ctx, "david-pulumi-sg", &ec2.SecurityGroupArgs{
			Description: pulumi.String("Security group for ec2 Nodes"),
			Name:        pulumi.String("david-pulumi-sg"),
			VpcId:       vpc.ID(),

			Ingress: ec2.SecurityGroupIngressArray{
				ec2.SecurityGroupIngressArgs{
					Protocol:   pulumi.String("-1"),
					FromPort:   pulumi.Int(0),
					ToPort:     pulumi.Int(0),
					CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
				},
			},
			Egress: ec2.SecurityGroupEgressArray{
				ec2.SecurityGroupEgressArgs{
					Protocol:   pulumi.String("-1"),
					CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
					FromPort:   pulumi.Int(0),
					ToPort:     pulumi.Int(0),
				},
			},
		})

		if err != nil {
			return err
		}

		// Get the list of AZ's for the defined region
		azState := "available"
		zoneList, err := aws.GetAvailabilityZones(ctx, &aws.GetAvailabilityZonesArgs{
			State: &azState,
		})

		if err != nil {
			return err
		}

		//How many AZ's to spread nodes across. Default to 3.
		zoneNumber := 3
		zones := []string{"a", "b", "c"}

		var subnets []*ec2.Subnet

		// Iterate through the AZ's for the VPC and create a subnet in each
		for i := 0; i < zoneNumber; i++ {
			subnet, err := ec2.NewSubnet(ctx, "david-pulumi-subnet-"+strconv.Itoa(i), &ec2.SubnetArgs{
				AvailabilityZone:    pulumi.String(zoneList.Names[i]),
				Tags:                pulumi.StringMap{"Name": pulumi.String("david-pulumi-subnet-" + strconv.Itoa(i))},
				VpcId:               vpc.ID(),
				CidrBlock:           pulumi.String("10.0." + strconv.Itoa(i) + ".0/24"),
				MapPublicIpOnLaunch: pulumi.Bool(true),
			})

			if err != nil {
				return err
			}

			subnets = append(subnets, subnet)
		}

		// Add Route Table
		_, err = ec2.NewDefaultRouteTable(ctx, "david-pulumi-routetable", &ec2.DefaultRouteTableArgs{
			DefaultRouteTableId: vpc.DefaultRouteTableId,
			Routes: ec2.DefaultRouteTableRouteArray{
				ec2.DefaultRouteTableRouteInput(&ec2.DefaultRouteTableRouteArgs{
					CidrBlock: pulumi.String("0.0.0.0/0"),
					GatewayId: igw.ID(),
				}),
			},
		})

		if err != nil {
			return err
		}

		// Create a downstream RKE cluster in Rancher
		if createDownstreamCluster {

			var machineConfigs []*rancher2.MachineConfigV2
			var machinePools []*rancher2.ClusterV2RkeConfigMachinePoolArgs

			for i := 0; i < 3; i++ {

				machineConfig, err := rancher2.NewMachineConfigV2(ctx, "david-pulumi-downstream-"+strconv.Itoa(i), &rancher2.MachineConfigV2Args{
					GenerateName: pulumi.String("david-pulumi-machineconf-downstream-" + strconv.Itoa(i)),
					Amazonec2Config: &rancher2.MachineConfigV2Amazonec2ConfigArgs{
						Ami:            pulumi.String("ami-0ff4c8fb495a5a50d"),
						InstanceType:   pulumi.String(downstreamClusterEC2Size),
						Region:         pulumi.String("eu-west-2"),
						SecurityGroups: pulumi.StringArray{sg.Name},
						SubnetId:       subnets[i].ID(),
						VpcId:          vpc.ID(),
						Zone:           pulumi.String(zones[i]),
						RootSize:       pulumi.String("50"),
					},
				})

				if err != nil {
					return err
				}

				machineConfigs = append(machineConfigs, machineConfig)

				machinePool := &rancher2.ClusterV2RkeConfigMachinePoolArgs{
					CloudCredentialSecretName: cloudcredential.ID(),
					ControlPlaneRole:          pulumi.Bool(true),
					EtcdRole:                  pulumi.Bool(true),
					Name:                      pulumi.String("aio" + strconv.Itoa(i)),
					Quantity:                  pulumi.Int(1),
					WorkerRole:                pulumi.Bool(true),
					MachineConfig: &rancher2.ClusterV2RkeConfigMachinePoolMachineConfigArgs{
						Kind: machineConfigs[i].Kind,
						Name: machineConfigs[i].Name,
					},
				}

				machinePools = append(machinePools, machinePool)
			}

			cluster, err := rancher2.NewClusterV2(ctx, "davidh-pulumi-cluster-downstream", &rancher2.ClusterV2Args{
				CloudCredentialSecretName:           cloudcredential.ID(),
				KubernetesVersion:                   pulumi.String("v1.21.5+rke2r2"),
				Name:                                pulumi.String("david-pulumi-downstream"),
				DefaultClusterRoleForProjectMembers: pulumi.String("user"),
				RkeConfig: &rancher2.ClusterV2RkeConfigArgs{
					MachinePools: rancher2.ClusterV2RkeConfigMachinePoolArray{
						machinePools[0],
						machinePools[1],
						machinePools[2],
					},
				},
			})

			// Required to help sync objects
			clusterSync, err := rancher2.NewClusterSync(ctx, "david-clustersync", &rancher2.ClusterSyncArgs{
				ClusterId:    cluster.ClusterV1Id,
				WaitCatalogs: pulumi.Bool(true),
				// Wait a couple of minutes for the cluster to be up before installing addons.
				// This is because, sometimes, it takes a while for the catalog repos to install/be refreshed
				// as part of the cluster standup
				StateConfirm: pulumi.Int(15),
			})

			// Decide which addons to install
			installIstio := conf.GetBool("installIstio")
			installOPA := conf.GetBool("installOPA")
			installCIS := conf.GetBool("installCIS")
			installLogging := conf.GetBool("installLogging")
			installLonghorn := conf.GetBool("installLonghorn")
			installMonitoring := conf.GetBool("installMonitoring")

			// Need to handle if OPA and Istio are installed with Monitoring, if so
			// influence the order so that monitoring is always first, followed
			// by the other two, otherwise it will fail - both OPA and Istio "look"
			// for Prometheus/Grafana as part of the install, if Monitoring is being installed
			// at the same time then errors will occur

			// Install Istio and OPA Standalone if desired

			if installMonitoring == false {
				if installIstio {
					_, err := rancher2.NewAppV2(ctx, "istio-standalone", &rancher2.AppV2Args{
						ChartName: pulumi.String("rancher-istio"),
						ClusterId: cluster.ClusterV1Id,
						Namespace: pulumi.String("istio-system"),
						RepoName:  pulumi.String("rancher-charts"),
					}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

					if err != nil {
						return err
					}
				}

				if installOPA {
					_, err = rancher2.NewAppV2(ctx, "opa-standalone", &rancher2.AppV2Args{
						ChartName: pulumi.String("rancher-gatekeeper"),
						ClusterId: cluster.ClusterV1Id,
						Namespace: pulumi.String("opa-system"),
						RepoName:  pulumi.String("rancher-charts"),
					}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

					if err != nil {
						return err
					}
				}
			}

			// Handle dependency between Istio, OPA and Monitoring if desired
			if installMonitoring {
				monitoring, err := rancher2.NewAppV2(ctx, "monitoring", &rancher2.AppV2Args{
					ChartName: pulumi.String("rancher-monitoring"),
					ClusterId: cluster.ClusterV1Id,
					Namespace: pulumi.String("cattle-monitoring-system"),
					RepoName:  pulumi.String("rancher-charts"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}

				if installIstio {
					_, err = rancher2.NewAppV2(ctx, "istio-with-monitoring", &rancher2.AppV2Args{
						ChartName: pulumi.String("rancher-istio"),
						ClusterId: cluster.ClusterV1Id,
						Namespace: pulumi.String("istio-system"),
						RepoName:  pulumi.String("rancher-charts"),
						//ChartVersion: pulumi.String("1.8.300"),
					}, pulumi.DependsOn([]pulumi.Resource{clusterSync, monitoring}))

					if err != nil {
						return err
					}
				}

				if installOPA {
					_, err = rancher2.NewAppV2(ctx, "opa-with-monitoring", &rancher2.AppV2Args{
						ChartName: pulumi.String("rancher-gatekeeper"),
						ClusterId: cluster.ClusterV1Id,
						Namespace: pulumi.String("opa-system"),
						RepoName:  pulumi.String("rancher-charts"),
					}, pulumi.DependsOn([]pulumi.Resource{clusterSync, monitoring}))

					if err != nil {
						return err
					}
				}
			}

			if installCIS {
				_, err = rancher2.NewAppV2(ctx, "cis", &rancher2.AppV2Args{
					ChartName: pulumi.String("rancher-cis-benchmark"),
					ClusterId: cluster.ClusterV1Id,
					Namespace: pulumi.String("cis-system"),
					RepoName:  pulumi.String("rancher-charts"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

			if installLogging {
				_, err = rancher2.NewAppV2(ctx, "logging", &rancher2.AppV2Args{
					ChartName: pulumi.String("rancher-logging"),
					ClusterId: cluster.ClusterV1Id,
					Namespace: pulumi.String("cattle-logging-system"),
					RepoName:  pulumi.String("rancher-charts"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

			if installLonghorn {
				_, err = rancher2.NewAppV2(ctx, "longhorn", &rancher2.AppV2Args{
					ChartName: pulumi.String("longhorn"),
					ClusterId: cluster.ClusterV1Id,
					Namespace: pulumi.String("longhorn-system"),
					RepoName:  pulumi.String("rancher-charts"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

		}

		if installFleetClusters {

			// create some EC2 instances to install K3s on:
			for i := 0; i < 3; i++ {

				machineConfig, err := rancher2.NewMachineConfigV2(ctx, "david-pulumi-fleet-machineconf-"+strconv.Itoa(i), &rancher2.MachineConfigV2Args{
					GenerateName: pulumi.String("david-pulumi-fleet-machineconf-" + strconv.Itoa(i)),
					Amazonec2Config: &rancher2.MachineConfigV2Amazonec2ConfigArgs{
						Ami:            pulumi.String("ami-0ff4c8fb495a5a50d"),
						InstanceType:   pulumi.String(fleetClustersEC2Size),
						Region:         pulumi.String("eu-west-2"),
						SecurityGroups: pulumi.StringArray{sg.Name},
						SubnetId:       subnets[i].ID(),
						VpcId:          vpc.ID(),
						Zone:           pulumi.String(zones[i]),
					},
				})

				_, err = rancher2.NewClusterV2(ctx, "davidh-pulumi-cluster-"+strconv.Itoa(i), &rancher2.ClusterV2Args{
					CloudCredentialSecretName:           cloudcredential.ID(),
					KubernetesVersion:                   pulumi.String("v1.21.4+k3s1"),
					Name:                                pulumi.String("david-pulumi-fleet-" + strconv.Itoa(i)),
					DefaultClusterRoleForProjectMembers: pulumi.String("user"),
					RkeConfig: &rancher2.ClusterV2RkeConfigArgs{
						MachinePools: rancher2.ClusterV2RkeConfigMachinePoolArray{
							&rancher2.ClusterV2RkeConfigMachinePoolArgs{
								CloudCredentialSecretName: cloudcredential.ID(),
								ControlPlaneRole:          pulumi.Bool(true),
								EtcdRole:                  pulumi.Bool(true),
								Name:                      pulumi.String("aio"),
								Quantity:                  pulumi.Int(1),
								WorkerRole:                pulumi.Bool(true),
								MachineConfig: &rancher2.ClusterV2RkeConfigMachinePoolMachineConfigArgs{
									Kind: machineConfig.Kind,
									Name: machineConfig.Name,
								},
							},
						},
					},
				})

				if err != nil {
					return err
				}
			}

		}

		// End return
		return nil
	})

}
