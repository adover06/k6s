/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command k6s-tui is a terminal dashboard for k6s Tenants. It lists every
// Tenant in the cluster with its quota, admins, deletion policy, namespace
// health and Ready status, and refreshes on a timer. Read-only: it never
// mutates the cluster.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/adover06/k6s/api/v1alpha1"
)

const refreshEvery = 3 * time.Second

// ---- palette ----------------------------------------------------------------

var (
	accent  = lipgloss.Color("39")  // bright blue
	accent2 = lipgloss.Color("213") // pink
	good    = lipgloss.Color("42")  // green
	bad     = lipgloss.Color("203") // red
	warn    = lipgloss.Color("214") // amber
	dim     = lipgloss.Color("244")
	fg      = lipgloss.Color("231")

	logoStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	subStyle    = lipgloss.NewStyle().Foreground(accent2)
	dimStyle    = lipgloss.NewStyle().Foreground(dim)
	errStyle    = lipgloss.NewStyle().Foreground(bad).Bold(true)
	goodStyle   = lipgloss.NewStyle().Foreground(good)
	emptyStyle  = lipgloss.NewStyle().Foreground(dim).Italic(true).Padding(1, 2)
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)
	footerStyle = lipgloss.NewStyle().Foreground(dim).Padding(0, 1)
)

// ---- k8s client -------------------------------------------------------------

func newClient() (client.Client, string, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, "", err
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		return nil, "", err
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("connecting to cluster: %w", err)
	}
	return cl, cfg.Host, nil
}

// ---- messages ---------------------------------------------------------------

type tenantRow struct {
	name      string
	namespace string
	nsExists  bool
	cpu, mem  string
	pods      int32
	admins    int
	policy    string
	ready     string
}

type (
	tenantsMsg struct{ rows []tenantRow }
	errMsg     struct{ err error }
	tickMsg    time.Time
)

// ---- model ------------------------------------------------------------------

type model struct {
	cl       client.Client
	host     string
	table    table.Model
	rows     []tenantRow
	err      error
	lastSync time.Time
	width    int
	height   int
}

func newModel(cl client.Client, host string) model {
	columns := []table.Column{
		{Title: "TENANT", Width: 16},
		{Title: "NAMESPACE", Width: 22},
		{Title: "CPU", Width: 6},
		{Title: "MEM", Width: 8},
		{Title: "PODS", Width: 6},
		{Title: "ADMINS", Width: 7},
		{Title: "POLICY", Width: 8},
		{Title: "READY", Width: 7},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	st := table.DefaultStyles()
	st.Header = st.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(accent).
		Bold(true).
		Foreground(fg)
	st.Selected = st.Selected.Foreground(lipgloss.Color("16")).Background(accent).Bold(true)
	st.Cell = st.Cell.Foreground(fg)
	t.SetStyles(st)
	return model{cl: cl, host: host, table: t}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), tickCmd())
}

func (m model) fetchCmd() tea.Cmd {
	cl := m.cl
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var list platformv1alpha1.TenantList
		if err := cl.List(ctx, &list); err != nil {
			return errMsg{err}
		}
		rows := make([]tenantRow, 0, len(list.Items))
		for i := range list.Items {
			t := &list.Items[i]
			nsName := "tenant-" + t.Name
			nsExists := true
			if err := cl.Get(ctx, types.NamespacedName{Name: nsName}, &corev1.Namespace{}); apierrors.IsNotFound(err) {
				nsExists = false
			}
			ready := "—"
			for _, c := range t.Status.Conditions {
				if c.Type == "Ready" {
					ready = string(c.Status)
				}
			}
			policy := t.Spec.DeletionPolicy
			if policy == "" {
				policy = platformv1alpha1.DeletionPolicyRetain
			}
			rows = append(rows, tenantRow{
				name:      t.Name,
				namespace: nsName,
				nsExists:  nsExists,
				cpu:       t.Spec.Quota.CPU.String(),
				mem:       t.Spec.Quota.Memory.String(),
				pods:      t.Spec.Quota.Pods,
				admins:    len(t.Spec.Admins),
				policy:    string(policy),
				ready:     ready,
			})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		return tenantsMsg{rows}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			return m, m.fetchCmd()
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h := m.height - 9
		if h < 3 {
			h = 3
		}
		m.table.SetHeight(h)
	case tenantsMsg:
		m.rows = msg.rows
		m.err = nil
		m.lastSync = time.Now()
		m.table.SetRows(toTableRows(msg.rows))
	case errMsg:
		m.err = msg.err
	case tickMsg:
		return m, tea.Batch(m.fetchCmd(), tickCmd())
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func toTableRows(rows []tenantRow) []table.Row {
	out := make([]table.Row, 0, len(rows))
	for _, r := range rows {
		nsCell := r.namespace + "  ✓"
		if !r.nsExists {
			nsCell = r.namespace + "  ✗"
		}
		out = append(out, table.Row{
			r.name,
			nsCell,
			r.cpu,
			r.mem,
			fmt.Sprintf("%d", r.pods),
			fmt.Sprintf("%d", r.admins),
			r.policy,
			r.ready,
		})
	}
	return out
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n\n")

	if len(m.rows) == 0 && m.err == nil {
		b.WriteString(emptyStyle.Render("No Tenants yet.\nApply one with:  kubectl apply -f config/samples/platform_v1alpha1_tenant.yaml"))
	} else {
		b.WriteString(m.table.View())
	}

	b.WriteString("\n")
	b.WriteString(m.footerView())
	return b.String()
}

func (m model) headerView() string {
	title := logoStyle.Render("▚▚ k6s") + subStyle.Render("  ·  tenant control")

	synced := "never"
	if !m.lastSync.IsZero() {
		synced = m.lastSync.Format("15:04:05")
	}
	meta := dimStyle.Render(fmt.Sprintf(
		"cluster %s   tenants %d   synced %s   ↻ %ds",
		shorten(m.host, 34), len(m.rows), synced, int(refreshEvery.Seconds()),
	))

	inner := lipgloss.JoinVertical(lipgloss.Left, title, meta)
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	return borderStyle.Width(w).Render(inner)
}

func (m model) footerView() string {
	keys := footerStyle.Render("↑/↓ navigate   r refresh   q quit")
	if m.err != nil {
		return keys + "\n" + errStyle.Render("error: "+m.err.Error())
	}
	healthy := 0
	for _, r := range m.rows {
		if r.nsExists {
			healthy++
		}
	}
	legend := footerStyle.Render(goodStyle.Render("✓ namespace present") + dimStyle.Render("   ✗ missing") +
		dimStyle.Render(fmt.Sprintf("   %d/%d healthy", healthy, len(m.rows))))
	return keys + "\n" + legend
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func main() {
	// silence controller-runtime's logger warning on stderr for a clean TUI.
	_ = warn

	cl, host, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "k6s-tui:", err)
		os.Exit(1)
	}
	if _, err := tea.NewProgram(newModel(cl, host), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "k6s-tui:", err)
		os.Exit(1)
	}
}
