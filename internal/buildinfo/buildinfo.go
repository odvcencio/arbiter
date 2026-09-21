package buildinfo

const (
	Product                 = "arbiter"
	Version                 = "1.10.0"
	OperatorContractVersion = "operator.v1"
)

type OperatorInfo struct {
	Product                 string `json:"product"`
	BuildVersion            string `json:"build_version"`
	OperatorContractVersion string `json:"operator_contract_version"`
}

type OperatorCompatibility struct {
	Available                       bool   `json:"available"`
	Compatible                      bool   `json:"compatible"`
	ExpectedProduct                 string `json:"expected_product"`
	ExpectedOperatorContractVersion string `json:"expected_operator_contract_version"`
	Product                         string `json:"product,omitempty"`
	BuildVersion                    string `json:"build_version,omitempty"`
	OperatorContractVersion         string `json:"operator_contract_version,omitempty"`
	Reason                          string `json:"reason,omitempty"`
}

func Current() OperatorInfo {
	return OperatorInfo{
		Product:                 Product,
		BuildVersion:            Version,
		OperatorContractVersion: OperatorContractVersion,
	}
}

func CheckOperator(operator OperatorInfo) OperatorCompatibility {
	report := OperatorCompatibility{
		ExpectedProduct:                 Product,
		ExpectedOperatorContractVersion: OperatorContractVersion,
		Product:                         operator.Product,
		BuildVersion:                    operator.BuildVersion,
		OperatorContractVersion:         operator.OperatorContractVersion,
	}
	if operator.Product == "" && operator.BuildVersion == "" && operator.OperatorContractVersion == "" {
		report.Reason = "operator identity unavailable"
		return report
	}
	report.Available = true
	if operator.Product != Product {
		report.Reason = "unexpected product"
		return report
	}
	if operator.OperatorContractVersion != OperatorContractVersion {
		report.Reason = "unexpected operator contract version"
		return report
	}
	report.Compatible = true
	return report
}
