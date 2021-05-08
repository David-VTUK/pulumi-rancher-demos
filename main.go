package main

import (
	"fmt"
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
			// Create AWS Cloud Credential
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

			// Create a Node Template - one for each AZ
			var nodetemplates []*rancher2.NodeTemplate

			for i := 0; i < zoneNumber; i++ {
				nodetemplate, err := rancher2.NewNodeTemplate(ctx, "david-pulumi-nodetemplate-"+zoneList.Names[i], &rancher2.NodeTemplateArgs{
					CloudCredentialId: cloudcredential.ID(),
					Description:       pulumi.String("node template for ec2"),
					Name:              pulumi.String("david-pulumi-nodetemplate-" + zones[i]),
					EngineInstallUrl:  pulumi.String("https://releases.rancher.com/install-docker/19.03.sh"),

					Amazonec2Config: &rancher2.NodeTemplateAmazonec2ConfigArgs{
						Ami:            pulumi.String("ami-0ff4c8fb495a5a50d"),
						InstanceType:   pulumi.String(downstreamClusterEC2Size),
						VpcId:          vpc.ID(),
						RootSize:       pulumi.String("50"),
						SecurityGroups: pulumi.StringArray{sg.Name},
						Region:         pulumi.String("eu-west-2"),
						Zone:           pulumi.String(zones[i]),
					},
				})

				if err != nil {
					return err
				}

				nodetemplates = append(nodetemplates, nodetemplate)
			}

			// Create Cluster
			cluster, err := rancher2.NewCluster(ctx, "david-pulumi-cluster", &rancher2.ClusterArgs{
				Description: pulumi.String("Cluster created by Pulumi"),
				Driver:      pulumi.String("rancherKubernetesEngine"),
				Name:        pulumi.String("david-pulumi-cluster"),
				RkeConfig: &rancher2.ClusterRkeConfigArgs{
					Network: &rancher2.ClusterRkeConfigNetworkArgs{
						Plugin: pulumi.String("canal"),
					},
				},
			}, pulumi.DependsOn([]pulumi.Resource{nodetemplates[0], nodetemplates[1], nodetemplates[2]}))

			if err != nil {
				return err
			}

			// Create nodepools to match to each region
			var nodepools []*rancher2.NodePool

			for i := 0; i < zoneNumber; i++ {
				nodepool, err := rancher2.NewNodePool(ctx, "david-pulumi-nodepool-"+strconv.Itoa(i), &rancher2.NodePoolArgs{
					ClusterId:      cluster.ID(),
					ControlPlane:   pulumi.Bool(true),
					Etcd:           pulumi.Bool(true),
					HostnamePrefix: pulumi.String("david-pulumi-node-"),
					Name:           pulumi.String("david-pulumi-pool-" + strconv.Itoa(i)),
					Quantity:       pulumi.Int(1),
					Worker:         pulumi.Bool(true),
					NodeTemplateId: nodetemplates[i].ID(),
				})
				if err != nil {
					return err
				}
				nodepools = append(nodepools, nodepool)

			}

			// Required to help sync objects
			clusterSync, err := rancher2.NewClusterSync(ctx, "david-clustersync", &rancher2.ClusterSyncArgs{
				ClusterId:   cluster.ID(),
				NodePoolIds: pulumi.StringArray{nodepools[0].ID(), nodepools[1].ID(), nodepools[2].ID()},
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

			if installIstio {
				_, err := rancher2.NewAppV2(ctx, "istio", &rancher2.AppV2Args{
					ChartName:    pulumi.String("rancher-istio"),
					ClusterId:    cluster.ID(),
					Namespace:    pulumi.String("istio-system"),
					RepoName:     pulumi.String("rancher-charts"),
					ChartVersion: pulumi.String("1.8.300"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}

			}

			if installOPA {
				_, err = rancher2.NewAppV2(ctx, "opa", &rancher2.AppV2Args{
					ChartName:    pulumi.String("rancher-gatekeeper"),
					ClusterId:    cluster.ID(),
					Namespace:    pulumi.String("opa-system"),
					RepoName:     pulumi.String("rancher-charts"),
					ChartVersion: pulumi.String("3.3.000"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

			if installCIS {
				_, err = rancher2.NewAppV2(ctx, "cis", &rancher2.AppV2Args{
					ChartName:    pulumi.String("rancher-cis-benchmark"),
					ClusterId:    cluster.ID(),
					Namespace:    pulumi.String("cis-system"),
					RepoName:     pulumi.String("rancher-charts"),
					ChartVersion: pulumi.String("1.0.301"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

			if installLogging {
				_, err = rancher2.NewAppV2(ctx, "logging", &rancher2.AppV2Args{
					ChartName:    pulumi.String("rancher-logging"),
					ClusterId:    cluster.ID(),
					Namespace:    pulumi.String("cattle-logging-system"),
					RepoName:     pulumi.String("rancher-charts"),
					ChartVersion: pulumi.String("3.9.000"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

			if installLonghorn {
				_, err = rancher2.NewAppV2(ctx, "longhorn", &rancher2.AppV2Args{
					ChartName:    pulumi.String("longhorn"),
					ClusterId:    cluster.ID(),
					Namespace:    pulumi.String("longhorn-system"),
					RepoName:     pulumi.String("rancher-charts"),
					ChartVersion: pulumi.String("1.1.001"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}

			if installMonitoring {
				_, err := rancher2.NewAppV2(ctx, "monitoring", &rancher2.AppV2Args{
					ChartName:    pulumi.String("rancher-monitoring"),
					ClusterId:    cluster.ID(),
					Namespace:    pulumi.String("cattle-monitoring-system"),
					RepoName:     pulumi.String("rancher-charts"),
					ChartVersion: pulumi.String("9.4.203"),
				}, pulumi.DependsOn([]pulumi.Resource{clusterSync}))

				if err != nil {
					return err
				}
			}
		}

		if installFleetClusters {
			// create some EC2 instances to install K3s on:
			for i := 0; i < 3; i++ {
				cluster, _ := rancher2.NewCluster(ctx, "david-pulumi-fleet-"+strconv.Itoa(i), &rancher2.ClusterArgs{
					Name: pulumi.String("david-pulumi-fleet-" + strconv.Itoa(i)),
				})

				joincommand := cluster.ClusterRegistrationToken.Command().ApplyT(func(command *string) string {
					getPublicIP := "IP=$(curl -H \"X-aws-ec2-metadata-token: $TOKEN\" -v http://169.254.169.254/latest/meta-data/public-ipv4)"
					installK3s := "curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=v1.19.5+k3s2 INSTALL_K3S_EXEC=\"--node-external-ip $IP\" sh -"
					nodecommand := fmt.Sprintf("#!/bin/bash\n%s\n%s\n%s", getPublicIP, installK3s, *command)
					return nodecommand
				}).(pulumi.StringOutput)

				_, err = ec2.NewInstance(ctx, "david-pulumi-fleet-node-"+strconv.Itoa(i), &ec2.InstanceArgs{
					Ami:                 pulumi.String("ami-0ff4c8fb495a5a50d"),
					InstanceType:        pulumi.String(fleetClustersEC2Size),
					KeyName:             pulumi.String("davidh-keypair"),
					VpcSecurityGroupIds: pulumi.StringArray{sg.ID()},
					UserData:            joincommand,
					SubnetId:            subnets[i].ID(),
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
