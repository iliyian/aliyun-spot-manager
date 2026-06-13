package aliyun

import (
	"fmt"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/bssopenapi"
	log "github.com/sirupsen/logrus"
)

// BillingItem represents a billing item for an instance
type BillingItem struct {
	InstanceID            string  // 实例ID
	InstanceName          string  // 实例名称 (ProductDetail)
	Region                string  // 区域
	ProductCode           string  // 产品代码 (ecs)
	ProductDetail         string  // 产品明细
	BillingItemName       string  // 计费项名称 (实例规格、系统盘、数据盘、公网带宽等)
	InstanceSpec          string  // 实例规格 (ecs.t6-c4m1.large)
	PretaxAmount          float64 // 应付金额
	CashAmount            float64 // 现金支付
	DeductedByCoupons     float64 // 优惠券抵扣
	DeductedByCashCoupons float64 // 代金券抵扣
	DeductedByPrepaidCard float64 // 储值卡抵扣
	Currency              string  // 货币单位
}

// InstanceBillingSummary represents billing summary for a single instance
type InstanceBillingSummary struct {
	InstanceID      string
	InstanceName    string
	Region          string
	AccountLabel    string
	InstanceSpec    string // 实例规格
	Items           []BillingItem
	TotalAmount     float64 // 实际费用（含抵扣）
	TotalCashAmount float64 // 现金支付总额
	TotalDeductions float64 // 各项抵扣总额
	RunningHours    float64 // 运行小时数（计算资源 ServicePeriod）
	HourlyCost      float64 // 平均每小时费用（各计费项小时成本之和）
}

// BillingSummary represents the billing summary for the current month
type BillingSummary struct {
	StartTime         time.Time
	EndTime           time.Time
	BillingCycle      string  // 账单周期 (YYYY-MM)
	AccountLabel      string  // 账号标签
	ElapsedDays       int     // 本月已过天数
	TotalRunningHours float64 // 总运行小时数
	Instances         []InstanceBillingSummary
	TotalAmount       float64 // 实际费用合计（含抵扣）
	TotalCashAmount   float64 // 现金支付合计
	TotalDeductions   float64 // 各项抵扣合计
	MonthlyEstimate   float64 // 月度估算
	EstimateMethod    string  // 估算方法说明
}

// BillingClient wraps the Aliyun BSS client
type BillingClient struct {
	client *bssopenapi.Client
}

// NewBillingClient creates a new BSS client
func NewBillingClient(accessKeyID, accessKeySecret string) (*BillingClient, error) {
	// BSS API uses cn-hangzhou as the default region
	client, err := bssopenapi.NewClientWithAccessKey("cn-hangzhou", accessKeyID, accessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create BSS client: %w", err)
	}

	return &BillingClient{
		client: client,
	}, nil
}

// InstanceInfo contains basic instance information for billing display
type InstanceInfo struct {
	InstanceID   string
	InstanceName string
	RegionID     string
}

// QueryBilling queries billing for the specified instances for the current month
// Note: Aliyun API returns monthly cumulative data, so we query the current month's data
// and calculate monthly estimate based on per-item hourly costs summed together
func (c *BillingClient) QueryBilling(instances []InstanceInfo, accountLabel string) (*BillingSummary, error) {
	now := time.Now()
	// Start of current month
	startTime := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	log.Debugf("[%s] Querying billing for %d instances, current month %s",
		accountLabel, len(instances), now.Format("2006-01"))

	// Create instance ID to info map for quick lookup
	instanceMap := make(map[string]InstanceInfo)
	for _, inst := range instances {
		instanceMap[inst.InstanceID] = inst
	}

	// Query current month's billing cycle
	cycle := now.Format("2006-01")

	// Group billing items by instance
	instanceBillings := make(map[string]*InstanceBillingSummary)

	log.Debugf("[%s] Querying billing cycle: %s", accountLabel, cycle)

	// Query instance bill
	request := bssopenapi.CreateQueryInstanceBillRequest()
	request.Scheme = "https"
	request.BillingCycle = cycle
	request.ProductCode = "ecs"
	request.IsBillingItem = requests.NewBoolean(true)
	request.PageSize = requests.NewInteger(300)
	request.PageNum = requests.NewInteger(1)

	response, err := c.client.QueryInstanceBill(request)
	if err != nil {
		return nil, fmt.Errorf("failed to query instance bill for cycle %s: %w", cycle, err)
	}

	log.Debugf("[%s] Got %d billing items from API for cycle %s", accountLabel, len(response.Data.Items.Item), cycle)

	for _, item := range response.Data.Items.Item {
		// Skip if not in our instance list
		instInfo, exists := instanceMap[item.InstanceID]
		if !exists {
			continue
		}

		// 实际费用 = 应付金额 + 各项抵扣（抵用券/优惠券/储值卡）
		// 使用抵扣后 PretaxAmount 可能为 0，需加回抵扣部分得到实际成本
		actualAmount := item.PretaxAmount + item.DeductedByCoupons + item.DeductedByCashCoupons + item.DeductedByPrepaidCard

		// Debug log to see actual API response fields
		log.Debugf("[%s] Billing item: InstanceID=%s, InstanceSpec=%s, BillingItem=%s, ServicePeriod=%s, PretaxAmount=%.4f, Coupons=%.4f, CashCoupons=%.4f, PrepaidCard=%.4f, actual=%.4f",
			accountLabel, item.InstanceID, item.InstanceSpec, item.BillingItem, item.ServicePeriod, item.PretaxAmount, item.DeductedByCoupons, item.DeductedByCashCoupons, item.DeductedByPrepaidCard, actualAmount)

		summary, exists := instanceBillings[item.InstanceID]
		if !exists {
			summary = &InstanceBillingSummary{
				InstanceID:   item.InstanceID,
				InstanceName: instInfo.InstanceName,
				Region:       instInfo.RegionID,
				AccountLabel: accountLabel,
				InstanceSpec: item.InstanceSpec,
				Items:        []BillingItem{},
				TotalAmount:  0,
			}
			instanceBillings[item.InstanceID] = summary
		}

		// Update InstanceSpec if not set
		if summary.InstanceSpec == "" && item.InstanceSpec != "" {
			summary.InstanceSpec = item.InstanceSpec
		}

		// 逐项计算小时成本：每个计费项用自己的 ServicePeriod 算
		// 系统盘（24/7计费）和计算资源（仅在运行时计费）的 ServicePeriod 不同
		if item.ServicePeriod != "" && item.ServicePeriodUnit == "秒" {
			if seconds, err := parseServicePeriod(item.ServicePeriod, item.ServicePeriodUnit); err == nil && seconds > 0 {
				// 该计费项的小时成本
				itemHourly := actualAmount / (seconds / 3600)
				summary.HourlyCost += itemHourly

				// 跟踪运行时长用于展示：取最大的 ServicePeriod（通常对应系统盘）
				if seconds > summary.RunningHours*3600 {
					summary.RunningHours = seconds / 3600
				}

				log.Debugf("[%s]   -> itemHourly=%.6f (amount=%.6f, seconds=%.0f)", accountLabel, itemHourly, actualAmount, seconds)
			}
		}

		// Format billing item name with InstanceSpec for compute resources
		billingItemName := formatBillingItemName(item.BillingItem, item.InstanceSpec)

		totalDeduction := item.DeductedByCoupons + item.DeductedByCashCoupons + item.DeductedByPrepaidCard

		billingItem := BillingItem{
			InstanceID:            item.InstanceID,
			InstanceName:          instInfo.InstanceName,
			Region:                instInfo.RegionID,
			ProductCode:           item.ProductCode,
			ProductDetail:         item.ProductDetail,
			BillingItemName:       billingItemName,
			InstanceSpec:          item.InstanceSpec,
			PretaxAmount:          item.PretaxAmount,
			CashAmount:            item.CashAmount,
			DeductedByCoupons:     item.DeductedByCoupons,
			DeductedByCashCoupons: item.DeductedByCashCoupons,
			DeductedByPrepaidCard: item.DeductedByPrepaidCard,
			Currency:              item.Currency,
		}

		summary.Items = append(summary.Items, billingItem)
		summary.TotalAmount += actualAmount
		summary.TotalCashAmount += item.CashAmount
		summary.TotalDeductions += totalDeduction
	}

	// Calculate total running seconds from per-instance data
	var totalRunningSeconds float64
	for _, summary := range instanceBillings {
		totalRunningSeconds += summary.RunningHours * 3600
	}

	// Calculate elapsed days this month
	elapsedDays := now.Day()
	totalRunningHours := totalRunningSeconds / 3600

	// Build final summary
	result := &BillingSummary{
		StartTime:         startTime,
		EndTime:           now,
		BillingCycle:      cycle,
		AccountLabel:      accountLabel,
		ElapsedDays:       elapsedDays,
		TotalRunningHours: totalRunningHours,
		Instances:         make([]InstanceBillingSummary, 0, len(instanceBillings)),
		TotalAmount:       0,
	}

	for _, summary := range instanceBillings {
		result.Instances = append(result.Instances, *summary)
		result.TotalAmount += summary.TotalAmount
		result.TotalCashAmount += summary.TotalCashAmount
		result.TotalDeductions += summary.TotalDeductions
	}

	// Calculate monthly estimate based on sum of per-instance hourly costs
	// Each instance's HourlyCost = sum of per-item hourly costs (each item ÷ its own ServicePeriod)
	// This assumes all instances run 24/7 for a full month
	var totalHourlyCost float64
	for _, inst := range result.Instances {
		if inst.HourlyCost > 0 {
			totalHourlyCost += inst.HourlyCost
		}
	}

	if totalHourlyCost > 0 {
		// Sum of all instance hourly costs × 730 hours (365/12*24, industry standard)
		result.MonthlyEstimate = totalHourlyCost * 730
		result.EstimateMethod = fmt.Sprintf("按每小时费用总和: ¥%.4f/小时 × 730小时", totalHourlyCost)
	} else if result.TotalAmount > 0 {
		// Fallback: use elapsed days this month
		if elapsedDays > 0 {
			dailyRate := result.TotalAmount / float64(elapsedDays)
			result.MonthlyEstimate = dailyRate * 30
			result.EstimateMethod = fmt.Sprintf("按已过天数: ¥%.4f/天 × 30天", dailyRate)
		}
	}

	log.Infof("[%s] Found billing for %d instances, total: %.4f, cash: %.4f, deductions: %.4f, running hours: %.2f, monthly estimate: %.2f",
		accountLabel, len(result.Instances), result.TotalAmount, result.TotalCashAmount, result.TotalDeductions, totalRunningHours, result.MonthlyEstimate)

	return result, nil
}

// QueryBillingByHours is deprecated, use QueryBilling instead
// Kept for backward compatibility
func (c *BillingClient) QueryBillingByHours(instances []InstanceInfo, hours int) (*BillingSummary, error) {
	return c.QueryBilling(instances, "")
}

// parseServicePeriod parses ServicePeriod string and converts to seconds based on unit
func parseServicePeriod(servicePeriod, unit string) (float64, error) {
	var value float64
	_, err := fmt.Sscanf(servicePeriod, "%f", &value)
	if err != nil {
		return 0, err
	}

	// Convert to seconds based on unit
	switch unit {
	case "天":
		return value * 24 * 3600, nil // days to seconds
	case "小时":
		return value * 3600, nil // hours to seconds
	case "秒", "":
		return value, nil // already in seconds
	default:
		// Assume seconds if unknown unit
		return value, nil
	}
}

// parseServicePeriodSeconds parses ServicePeriod string as seconds (deprecated, use parseServicePeriod)
func parseServicePeriodSeconds(servicePeriod string) (float64, error) {
	var seconds float64
	_, err := fmt.Sscanf(servicePeriod, "%f", &seconds)
	return seconds, err
}

// formatBillingItemName formats the billing item name for display
// For compute resources, it includes the instance spec (SKU)
func formatBillingItemName(billingItem, instanceSpec string) string {
	// Map common billing item names to friendly display names
	switch billingItem {
	case "系统盘":
		return "系统盘"
	case "数据盘":
		return "数据盘"
	case "云服务器配置":
		// For compute resources, show the specific SKU
		if instanceSpec != "" {
			return fmt.Sprintf("计算 (%s)", instanceSpec)
		}
		return "计算资源"
	case "ImageOS":
		return "镜像费用"
	case "公网带宽":
		return "公网带宽"
	case "流量":
		return "公网流量"
	case "快照":
		return "快照"
	case "实例":
		if instanceSpec != "" {
			return fmt.Sprintf("实例 (%s)", instanceSpec)
		}
		return "实例"
	default:
		if billingItem != "" {
			return billingItem
		}
		return "其他费用"
	}
}
