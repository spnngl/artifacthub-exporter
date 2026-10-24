package artifacthub

// Only the fields the exporter reads are declared.

// RepositorySummary is the repository of a package, as the API embeds it.
type RepositorySummary struct {
	ID                string `json:"repository_id"`
	Kind              Kind   `json:"kind"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	VerifiedPublisher bool   `json:"verified_publisher"`
	Official          bool   `json:"official"`
	OrganizationName  string `json:"organization_name"`
	UserAlias         string `json:"user_alias"`
}

// Publisher returns the organization's name, or the user's alias for a
// repository a user owns.
func (r RepositorySummary) Publisher() string {
	if r.OrganizationName != "" {
		return r.OrganizationName
	}
	return r.UserAlias
}

// Repository is a repository with its tracking and scanning status.
type Repository struct {
	RepositorySummary
	// LastTrackingTS and LastScanningTS are Unix timestamps, 0 when it never ran.
	LastTrackingTS     int64  `json:"last_tracking_ts"`
	LastTrackingErrors string `json:"last_tracking_errors"`
	LastScanningTS     int64  `json:"last_scanning_ts"`
	LastScanningErrors string `json:"last_scanning_errors"`
	Disabled           bool   `json:"disabled"`
}

// SecurityReportSummary counts the vulnerabilities of a package's report by severity.
type SecurityReportSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

// PackageSummary is what package search returns for a package.
type PackageSummary struct {
	ID         string `json:"package_id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	AppVersion string `json:"app_version"`
	License    string `json:"license"`
	Stars      int    `json:"stars"`
	// TS is the Unix timestamp of the latest release.
	TS                           int64 `json:"ts"`
	Deprecated                   bool  `json:"deprecated"`
	Signed                       bool  `json:"signed"`
	Official                     bool  `json:"official"`
	ProductionOrganizationsCount int   `json:"production_organizations_count"`
	// SecurityReportSummary is nil when the package has no security report.
	SecurityReportSummary *SecurityReportSummary `json:"security_report_summary"`
	Repository            RepositorySummary      `json:"repository"`
}

// PackageDetails is what the package's own endpoint returns beyond what search
// does. It has no star count.
type PackageDetails struct {
	AvailableVersions []struct {
		Version string `json:"version"`
	} `json:"available_versions"`
	Stats struct {
		Subscriptions int `json:"subscriptions"`
		Webhooks      int `json:"webhooks"`
	} `json:"stats"`
}

// Views is the views of a package: per version, per day (`YYYY-MM-DD`, UTC).
type Views map[string]map[string]int

// Daily sums the views of every version for each day.
func (v Views) Daily() map[string]int {
	daily := make(map[string]int)
	for _, days := range v {
		for day, n := range days {
			daily[day] += n
		}
	}
	return daily
}

// HubStats is what the hub holds in total.
type HubStats struct {
	Packages int `json:"packages"`
	Releases int `json:"releases"`
}
