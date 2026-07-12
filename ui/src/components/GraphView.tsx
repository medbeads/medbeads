import { useMemo, useCallback, useState, useEffect } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  MarkerType,
  Position,
  ReactFlowProvider,
  BaseEdge,
  EdgeLabelRenderer,
  useReactFlow,
  type Node,
  type Edge,
  type EdgeProps,
} from 'reactflow';
import 'reactflow/dist/style.css';
import {
  type TimelineItem,
  type PatientGraph,
  type GraphBead,
  type GraphLinkSeverity,
  getViewerRoles,
} from '../lib/api';

interface GraphViewProps {
  // Legacy mode: date-grouped timeline items (parent edges only, client-side
  // clearance masking). Kept so any other caller of GraphView with the old
  // TimelineItem[] contract keeps working unchanged.
  items?: TimelineItem[];
  onNodeClick?: (item: TimelineItem) => void;
  selectedId?: string;
  // Bead ids the server already marked as restricted for the current viewer
  // (each TimelineItem's `restricted` flag — see R8b). Replaces the old
  // per-bead ClearanceRule[] map; only a boolean is needed for rendering.
  restrictedIds?: Set<string>;
  // R7 mode: the richer two-axis graph from GET /patients/{root}/graph
  // (vertical parent DAG + correction chains, horizontal clinical_links).
  // Clearance masking and link-endpoint status normalization are already
  // applied server-side (specs/R7_graph_view.md) — no client-side re-masking
  // needed for this mode. When `graph` is provided it takes precedence over
  // `items`.
  graph?: PatientGraph;
  onBeadClick?: (bead: GraphBead) => void;
  selectedBeadId?: string;
  // "Figure mode" (paper-figure capture, see requirements at call site):
  // renders larger, text-labeled Bead tiles and boosted-opacity/width
  // clinical_link arcs so a static screenshot is legible without hovering.
  // Opt-in, defaults to false (normal interactive view unchanged). Only
  // affects BeadGraphView (the `graph` prop path) — LegacyGraphView ignores
  // it, since figure mode is defined against the R7c Clinical Spine layout.
  figureMode?: boolean;
}

const nodeWidth = 180;
const nodeHeight = 70;
const xGap = 30;
const yGap = 120;

// Define nodeTypes outside component to avoid React Flow warning.
const nodeTypes = {};

// --- Warm paper-chart palette (R7c, "The Clinical Spine") ------------------
//
// background near #FCFCFA, ink #1A2027, spine/encounter indigo #3B3170,
// quiet arc #B8B2C8, warning arc #B45309, active #2F855A. Chosen to read as
// "a chart with relationships woven across time", not an EMR timeline.
const PAPER = {
  bg: '#FCFCFA',
  ink: '#1A2027',
  spine: '#3B3170',
  spineTint: '#EDEBF5',
  quietArc: '#B8B2C8',
  warningArc: '#B45309',
  activeAccent: '#2F855A',
};

// severity: info = thin pale indigo/grey (co-occurrence always reads as
// quiet), warning = amber, alert/critical = red (critical thicker still).
const SEVERITY_STYLE: Record<GraphLinkSeverity, { stroke: string; width: number; dashed?: boolean }> = {
  info: { stroke: PAPER.quietArc, width: 1 },
  warning: { stroke: PAPER.warningArc, width: 2.25 },
  alert: { stroke: '#c2410c', width: 3 },
  critical: { stroke: '#9f1d1d', width: 4 },
};

// Figure-mode severity styling (requirement 2): print/screenshot legibility
// for a static capture. `info` is ~100% of the real clinical_links corpus
// (a DB CHECK constraint enforces severity='info' whenever
// evidence_basis='cooccurrence' — see migrations_0006_test.go), so in the
// interactive view it is intentionally faint (opacity 0.32, width 1) to keep
// the co-occurrence "haze" from drowning out true warning/alert/critical
// arcs. In a static figure that faintness reads as "no relationships at
// all". This table shifts the WHOLE scale up (stroke width AND a per-severity
// opacity) so info becomes a clearly-visible line while the ordering
// info < warning < alert < critical stays strictly increasing in both width
// and opacity — the legend stays truthful, only the baseline moved. Color
// per severity is unchanged (same semantic meaning as the interactive view).
const FIGURE_SEVERITY_STYLE: Record<GraphLinkSeverity, { stroke: string; width: number; opacity: number }> = {
  info: { stroke: PAPER.quietArc, width: 2, opacity: 0.75 },
  warning: { stroke: PAPER.warningArc, width: 3, opacity: 0.9 },
  alert: { stroke: '#c2410c', width: 4, opacity: 0.95 },
  critical: { stroke: '#9f1d1d', width: 5.5, opacity: 1 },
};

// status: active=green accent / amended=amber accent / retracted=strikethrough
// + muted / unattested=dashed border. '' (absent bead_status row) === active,
// per graph_test.go's documented "absent = active" fallback. Severity already
// carries color on the arcs, so status leans on border treatment + text
// decoration rather than competing fills (per design direction).
type NormalizedStatus = 'active' | 'amended' | 'retracted' | 'unattested';

function normalizeStatus(status: GraphBead['status']): NormalizedStatus {
  if (status === 'amended' || status === 'retracted' || status === 'unattested') return status;
  return 'active';
}

const STATUS_STYLE: Record<NormalizedStatus, { bg: string; border: string; borderStyle: string; borderWidth: string; text: string }> = {
  active: { bg: '#ffffff', border: PAPER.activeAccent, borderStyle: 'solid', borderWidth: '2px', text: PAPER.ink },
  amended: { bg: '#fffaf0', border: '#b7791f', borderStyle: 'double', borderWidth: '4px', text: PAPER.ink },
  retracted: { bg: '#f7f7f5', border: '#9a9a94', borderStyle: 'dashed', borderWidth: '2px', text: '#6b6b66' },
  unattested: { bg: '#ffffff', border: '#9a9a94', borderStyle: 'dashed', borderWidth: '2px', text: PAPER.ink },
};

function beadTypeLabel(type: string): string {
  const short = type.replace(/^fhir_/, '');
  const icons: Record<string, string> = {
    patient_registration: '🧾',
    encounter: '🩺',
    medication: '💊',
    medicationrequest: '💊',
    observation: '📈',
    condition: '⚠️',
    documentreference: '📄',
    diagnosticreport: '📄',
    clinical_note: '📝',
    procedure: '🔧',
    immunization: '💉',
    imagingstudy: '🖼️',
  };
  const icon = icons[short] ?? '🔹';
  return `${icon} ${short}`;
}

function truncate(s: string, max = 34): string {
  if (!s) return '';
  return s.length > max ? s.slice(0, max - 1) + '…' : s;
}

// Display-only: every bead.summary is server-formatted as "<bead.type>: <text>"
// (verified against the live API: 0/675 beads on a sample patient deviate
// from this — see graph_test.go / the flattener that produces `summary`).
// The tile already renders the type separately (bold, via beadTypeLabel), so
// showing the summary as-is burns close to half the tile's width on a
// duplicate of information already on screen (e.g. "fhir_observation:
// Cholesterol…" instead of "Cholesterol…"). This strips exactly that
// redundant "<type>: " prefix for rendering; it never touches `bead.summary`
// itself or anything sent back to the API — a stale/missing prefix (e.g. a
// future bead type this wasn't verified against) just falls through
// unchanged, so this is safe to apply unconditionally, not only in figure
// mode.
function displaySummary(bead: Pick<GraphBead, 'type' | 'summary'>): string {
  const prefix = `${bead.type}: `;
  return bead.summary.startsWith(prefix) ? bead.summary.slice(prefix.length) : bead.summary;
}

function formatLocalDate(iso: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString();
}

// --- Clinical-link edge data shape (horizontal axis, both legacy bezier and
// R7c arc renderers share this) -------------------------------------------
//
// Severity drives stroke color/width; relation + evidence_basis are shown on
// hover via a floating label (EdgeLabelRenderer), keeping the graph
// uncluttered at rest.
interface ClinicalLinkEdgeData {
  relation: string;
  matchedTag: string;
  severity: GraphLinkSeverity;
  evidenceBasis: string;
  // Provenance: the rule Bead that asserted this relationship, and the
  // projection run that wrote the row. Together they are the whole audit
  // claim — a rendered arc resolves to the immutable, content-addressed
  // knowledge Bead that produced it, via the run's projection_manifest entry.
  ruleVersion: string;
  projectionRunID: string;
  // Threaded through edge data (rather than component props) because
  // ArcLinkEdge is registered once in the static `spineEdgeTypes` map and
  // instantiated per-edge by ReactFlow itself — data is the only per-edge
  // channel available for a mode flag like this.
  figureMode: boolean;
}

// --- Legacy date-grouped layout (TimelineItem[] mode) -----------------------

const getLayoutedElements = (nodes: Node[], edges: Edge[], items: TimelineItem[], restrictedNodeIds: Set<string>) => {
  const nodesByDate: Record<string, Node[]> = {};

  nodes.forEach(node => {
    if (node.id.startsWith('clearance-group-')) return;

    const item = items.find(i => i.data.id === node.id);
    if (item && item.date) {
        const dateKey = item.date.length >= 10 ? item.date.substring(0, 10) : 'unknown';
        if (!nodesByDate[dateKey]) {
            nodesByDate[dateKey] = [];
        }
        nodesByDate[dateKey].push(node);
    } else {
        if (!nodesByDate['unknown']) nodesByDate['unknown'] = [];
        nodesByDate['unknown'].push(node);
    }
  });

  const sortedDates = Object.keys(nodesByDate).sort((a, b) => {
      if (a === 'unknown') return 1;
      if (b === 'unknown') return -1;
      return b.localeCompare(a);
  });

  let currentY = 0;

  sortedDates.forEach(date => {
    const rowNodes = nodesByDate[date];

    rowNodes.sort((a, b) => {
        const isRestrictedA = restrictedNodeIds.has(a.id);
        const isRestrictedB = restrictedNodeIds.has(b.id);

        if (isRestrictedA !== isRestrictedB) {
            return isRestrictedA ? 1 : -1;
        }

        const typeA = items.find(i => i.data.id === a.id)?.type || '';
        const typeB = items.find(i => i.data.id === b.id)?.type || '';
        return typeA.localeCompare(typeB);
    });

    rowNodes.forEach((node, index) => {
      node.position = {
        x: index * (nodeWidth + xGap),
        y: currentY
      };

      node.sourcePosition = Position.Bottom;
      node.targetPosition = Position.Top;
    });

    currentY += nodeHeight + yGap;
  });

  return { nodes, edges };
};

function LegacyGraphView({ items, onNodeClick, selectedId, restrictedIds = new Set() }: Required<Pick<GraphViewProps, 'items'>> & Omit<GraphViewProps, 'items' | 'graph'>) {
  const viewerRoles = getViewerRoles();

  const { nodes: initialNodes, edges: initialEdges, clearanceGroups } = useMemo(() => {
    const newNodes: Node[] = [];
    const newEdges: Edge[] = [];
    const idSet = new Set(items.map(item => item.data.id));
    const restrictedNodeIds = new Set<string>();

    const accessibleNodesByDate: Record<string, string[]> = {};

    items.forEach((item) => {
      const itemId = item.data.id;
      const isSelected = selectedId === itemId;
      const isRestricted = restrictedIds.has(itemId);

      if (isRestricted) {
          restrictedNodeIds.add(itemId);
      } else {
          const dateKey = item.date && item.date.length >= 10 ? item.date.substring(0, 10) : 'unknown';
          if (!accessibleNodesByDate[dateKey]) {
              accessibleNodesByDate[dateKey] = [];
          }
          accessibleNodesByDate[dateKey].push(itemId);
      }

      let label = item.type.toUpperCase();
      let bgColor = "rgba(255, 255, 255, 0.95)";
      let borderColor = "#cbd5e1";

      if (item.type === "medication") {
        label = "💊 Medication";
        bgColor = isSelected ? '#dbeafe' : '#f0fdf4';
        borderColor = isSelected ? '#2563eb' : '#16a34a';
      } else if (item.type === "observation") {
        label = "📈 Observation";
        bgColor = isSelected ? '#dbeafe' : '#f8fafc';
        borderColor = isSelected ? '#2563eb' : '#64748b';
      } else if (item.type === "encounter") {
        label = "🩺 Encounter";
        bgColor = isSelected ? '#dbeafe' : '#fff7ed';
        borderColor = isSelected ? '#2563eb' : '#f97316';
      } else if (item.type === "condition") {
        label = "⚠️ Condition";
        bgColor = isSelected ? '#dbeafe' : '#fef2f2';
        borderColor = isSelected ? '#2563eb' : '#dc2626';
      } else if (item.type === "diagnostic_report") {
        label = "📄 Report";
        bgColor = isSelected ? '#dbeafe' : '#f0f9ff';
        borderColor = isSelected ? '#2563eb' : '#0891b2';
      }

      if (isRestricted) {
          label = `🔒 ${label}`;
          bgColor = '#f1f5f9';
          borderColor = '#cbd5e1';
      }

      let detail = "";
      if (item.type === "medication") detail = item.data.medication_name;
      if (item.type === "observation") detail = item.data.display_name;
      if (item.type === "encounter") detail = item.data.encounter_type;
      if (item.type === "condition") detail = item.data.condition_name;
      if (item.type === "diagnostic_report") detail = item.data.title;

      if (detail && detail.length > 20) detail = detail.substring(0, 18) + "...";

      newNodes.push({
        id: itemId,
        data: { label: `${label}\n${detail}\n${new Date(item.date).toLocaleDateString()}` },
        position: { x: 0, y: 0 },
        style: {
            background: bgColor,
            border: `2px solid ${borderColor}`,
            color: isRestricted ? '#64748b' : '#1e293b',
            borderRadius: '10px',
            fontSize: '11px',
            padding: '8px',
            width: nodeWidth,
            textAlign: 'center' as const,
            whiteSpace: 'pre-wrap' as const,
            fontWeight: '500',
            boxShadow: isSelected ? '0 0 0 3px rgba(37, 99, 235, 0.3)' : '0 1px 3px rgba(0,0,0,0.1)',
            cursor: 'pointer',
            zIndex: 10,
            opacity: isRestricted ? 0.6 : 1.0,
        },
      });

      item.parents.forEach((parentId) => {
        if (idSet.has(parentId)) {
            newEdges.push({
                id: `e-${parentId}-${itemId}`,
                source: parentId,
                target: itemId,
                type: 'smoothstep',
                animated: false,
                style: { stroke: isRestricted ? '#e2e8f0' : '#94a3b8', strokeWidth: 1 },
                markerEnd: {
                    type: MarkerType.ArrowClosed,
                    color: isRestricted ? '#e2e8f0' : '#94a3b8',
                    width: 15,
                    height: 15,
                },
            });
        }
      });
    });

    const { nodes: layoutedNodes, edges: layoutedEdges } = getLayoutedElements(newNodes, newEdges, items, restrictedNodeIds);

    const groupNodes: Node[] = [];

    Object.entries(accessibleNodesByDate).forEach(([dateKey, nodeIds]) => {
      if (nodeIds.length === 0) return;

      const groupItemNodes = layoutedNodes.filter(n => nodeIds.includes(n.id));
      if (groupItemNodes.length === 0) return;

      const padding = 15;
      let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;

      groupItemNodes.forEach(node => {
        minX = Math.min(minX, node.position.x);
        minY = Math.min(minY, node.position.y);
        maxX = Math.max(maxX, node.position.x + nodeWidth);
        maxY = Math.max(maxY, node.position.y + nodeHeight);
      });

      const color = { bg: 'rgba(34, 197, 94, 0.05)', border: 'rgba(34, 197, 94, 0.3)' };

      groupNodes.push({
        id: `accessible-group-${dateKey}`,
        data: {
          label: "✓ Accessible Area",
        },
        position: { x: minX - padding, y: minY - padding - 20 },
        style: {
          width: maxX - minX + padding * 2,
          height: maxY - minY + padding * 2 + 20,
          background: color.bg,
          border: `2px dashed ${color.border}`,
          borderRadius: '12px',
          zIndex: -1,
          pointerEvents: 'none' as const,
          fontSize: '10px',
          fontWeight: '600',
          color: 'rgba(21, 128, 61, 0.8)',
          padding: '4px 8px',
          display: 'flex',
          alignItems: 'flex-start',
          justifyContent: 'flex-start',
        },
        selectable: false,
        draggable: false,
      });
    });

    const allNodes = [...groupNodes, ...layoutedNodes];

    return {
      nodes: allNodes,
      edges: layoutedEdges,
      clearanceGroups: Object.keys(accessibleNodesByDate)
    };
  }, [items, selectedId, restrictedIds]);

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    if (node.id.startsWith('clearance-group-')) return;

    const item = items.find(i => i.data.id === node.id);
    if (item && onNodeClick) onNodeClick(item);
  }, [items, onNodeClick]);

  return (
    <ReactFlow
      nodes={initialNodes}
      edges={initialEdges}
      nodeTypes={nodeTypes}
      onNodeClick={handleNodeClick}
      fitView
      fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
      minZoom={0.05}
      maxZoom={2}
      attributionPosition="bottom-right"
      proOptions={{ hideAttribution: true }}
    >
      <Background color="#e2e8f0" gap={20} size={1} />
      <Controls showInteractive={false} className="bg-white border border-slate-200 shadow-sm" />
      <MiniMap
          nodeColor={(n) => {
              if (n.id.startsWith('clearance-group-')) {
                return n.style?.background as string || 'rgba(251, 191, 36, 0.2)';
              }
              if (n.style?.background) return n.style.background as string;
              return '#fff';
          }}
          nodeBorderRadius={2}
          className="border border-slate-200 shadow-lg rounded-lg overflow-hidden"
      />

      <div className="absolute top-4 left-4 bg-white/90 backdrop-blur-sm rounded-lg shadow-md border border-slate-200 p-3 text-xs">
        <div className="font-semibold text-slate-700 mb-2">
          Security Clearance
          <span className="ml-2 font-normal text-slate-500">
            (Viewing as: {viewerRoles.join(', ')})
          </span>
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center gap-2">
            <div className="w-5 h-5 rounded border-2 border-dashed" style={{ background: 'rgba(34, 197, 94, 0.05)', borderColor: 'rgba(34, 197, 94, 0.3)' }} />
            <span className="text-slate-600 font-medium">Accessible Area</span>
          </div>
          <div className="flex items-center gap-2">
            <div className="w-5 h-5 rounded border-2 border-slate-300 bg-slate-100 flex items-center justify-center text-[10px]">
              🔒
            </div>
            <span className="text-slate-500 italic">Restricted (Outside Area)</span>
          </div>
        </div>
        {clearanceGroups.length > 0 && (
          <div className="mt-2 pt-2 border-t border-slate-200 text-slate-400 text-[10px]">
            {clearanceGroups.length} accessible section(s) identified
          </div>
        )}
      </div>
    </ReactFlow>
  );
}

// --- R7c "Clinical Spine" layout (PatientGraph mode) ------------------------
//
// Vertical axis = time spine: each fhir_encounter is rendered as a chapter
// card, stacked newest-at-top (matching LegacyGraphView's
// `b.localeCompare(a)` date-descending convention — see getLayoutedElements
// above). Non-encounter beads (observation/medication/note/...) are grouped
// under the encounter that is their `parent_id` in graph.edges and collapsed
// by default into a count badge — this is what kills the horizontal blowup
// from R7b (542 beads / 483 links exploding into a single wide row). A card
// expands on click to reveal its children grouped into per-type "islands"
// (condition / observation / medication / ...) laid out horizontally to the
// RIGHT of the chapter card — one island per type, each a small wrapped tile
// grid — rather than stacking all children in one tall vertical list (which
// breaks down for encounters with up to 40 children).
//
// patient_registration (and any bead with no parent edge at all — orphans)
// has no encounter to live under, so it is rendered as the root "chart
// opened" card at the very top of the spine, above the newest encounter.
const encounterCardWidth = 320;
const encounterCardMinHeight = 64;
const rootCardWidth = 260;
const spineGapCollapsed = 28;
const spineGapExpanded = 32;
const islandGap = 20; // horizontal gap between type-islands
const islandHeaderHeight = 18;
const islandPadding = 8;
// Fixed pixel margin the initial viewport pans in from the content origin
// (0,0), where the root/first chapter card sits — keeps it comfortably clear
// of the pane edge and Controls, without ever letting fitView re-center the
// graph and hide the start of the spine off-screen.
const spineViewportMargin = 40;

// Tile grid metrics (size of one Bead tile within an expanded island, and how
// many columns it wraps to). Two variants:
//  - default: dense, icon-only tiles (fit 3-per-row) for interactive use —
//    the full label only needs to be legible on hover (see BeadTile's doc
//    comment below).
//  - figure: requirement 1 — tiles must show readable text (type + summary)
//    with no hovering, so they are wider/taller and wrap to fewer columns.
interface TileMetrics {
  width: number;
  height: number;
  gapX: number;
  gapY: number;
  cols: number;
}

const TILE_METRICS: TileMetrics = { width: 96, height: 30, gapX: 6, gapY: 6, cols: 3 };
// Widened further (168->220) and taller (54->64) than the first figure-mode
// pass: with the redundant "<type>: " prefix now stripped (displaySummary)
// the clinical name is the dominant text, but names like "Asthma follow-up
// (regime/therapy changed)" or "Body mass index (BMI) [Percentile] Per
// age..." still need two full lines to read without truncation —
// "legibility beats density" per requirement 1, and cols stays at 2 so
// widening the tile doesn't blow out the per-chapter island row width.
const FIGURE_TILE_METRICS: TileMetrics = { width: 220, height: 64, gapX: 12, gapY: 12, cols: 2 };

function tileMetricsFor(figureMode: boolean): TileMetrics {
  return figureMode ? FIGURE_TILE_METRICS : TILE_METRICS;
}

interface TypeIsland {
  type: string; // short type, e.g. "observation"
  beads: GraphBead[];
}

interface SpineChapter {
  encounter: GraphBead;
  children: GraphBead[];
  // Grouped counts by short type (e.g. "observation" -> 12), for the
  // collapsed badge row ("12 observations · 2 medications · 1 note").
  counts: Array<{ type: string; count: number }>;
  // Same grouping as `counts` but carrying the actual beads, in clinical
  // display order, for the expanded horizontal-island layout.
  islands: TypeIsland[];
}

interface SpineLayout {
  // Root nodes with no encounter parent (patient_registration + orphans),
  // rendered above the first chapter.
  roots: GraphBead[];
  chapters: SpineChapter[];
  // y position (top) for every rendered node id: chapters, roots, and —
  // when expanded — child beads.
  positions: Record<string, { x: number; y: number }>;
  // Total height of the whole spine (for computing e.g. minimap bounds; not
  // strictly required by ReactFlow but useful for reasoning about layout).
  totalHeight: number;
}

function shortBeadType(type: string): string {
  return type.replace(/^fhir_/, '');
}

function pluralizeType(type: string, count: number): string {
  const short = shortBeadType(type).replace(/_/g, ' ');
  if (count === 1) return short;
  // Cheap English pluralization; good enough for FHIR-ish type nouns
  // (observation/medication/condition/note/procedure/immunization/...).
  if (short.endsWith('s')) return short;
  return `${short}s`;
}

// Clinically-natural reading order for type islands, left to right:
// condition first (what's being addressed), then the evidence
// (observation/report), then what was done (procedure), then what was
// prescribed (medication), then imaging/immunization/notes, then anything
// else alphabetically. Missing from this list falls through to the end.
const TYPE_ORDER = [
  'condition',
  'observation',
  'diagnosticreport',
  'documentreference',
  'procedure',
  'medicationrequest',
  'medication',
  'imagingstudy',
  'immunization',
  'clinical_note',
];

function typeOrderRank(type: string): number {
  const idx = TYPE_ORDER.indexOf(type);
  return idx === -1 ? TYPE_ORDER.length : idx;
}

// Height of one type-island's tile grid (header + wrapped NxM tiles), given
// its bead count — used both to position tiles and to reserve enough
// vertical room before the next chapter so islands never overlap.
function islandHeight(beadCount: number, tm: TileMetrics): number {
  const rows = Math.max(1, Math.ceil(beadCount / tm.cols));
  return islandHeaderHeight + rows * tm.height + Math.max(0, rows - 1) * tm.gapY + islandPadding * 2;
}

function islandWidth(beadCount: number, tm: TileMetrics): number {
  const cols = Math.min(tm.cols, Math.max(1, beadCount));
  return cols * tm.width + Math.max(0, cols - 1) * tm.gapX + islandPadding * 2;
}

function layoutBeadGraph(graph: PatientGraph, expandedIds: Set<string>, tm: TileMetrics): SpineLayout {
  const beadById = new Map(graph.beads.map((b) => [b.id, b]));

  // parent_id -> child beads, restricted to parents that are actually
  // fhir_encounter beads present in this graph (per DATA FACTS: child_id ->
  // parent_id, parent is the encounter).
  const childrenByEncounter: Record<string, GraphBead[]> = {};
  const hasParentEdge = new Set<string>();

  graph.edges.forEach((e) => {
    const child = beadById.get(e.child_id);
    const parent = beadById.get(e.parent_id);
    if (!child || !parent) return;
    hasParentEdge.add(e.child_id);
    if (parent.type !== 'fhir_encounter') return;
    (childrenByEncounter[e.parent_id] ??= []).push(child);
  });

  const encounters = graph.beads.filter((b) => b.type === 'fhir_encounter');
  // Newest-at-top, matching LegacyGraphView's date-descending convention.
  encounters.sort((a, b) => b.timestamp.localeCompare(a.timestamp) || b.id.localeCompare(a.id));

  const chapters: SpineChapter[] = encounters.map((encounter) => {
    const children = (childrenByEncounter[encounter.id] ?? []).slice().sort((a, b) => {
      return a.type.localeCompare(b.type) || a.timestamp.localeCompare(b.timestamp) || a.id.localeCompare(b.id);
    });

    const byType = new Map<string, GraphBead[]>();
    children.forEach((c) => {
      const t = shortBeadType(c.type);
      (byType.get(t) ?? byType.set(t, []).get(t)!).push(c);
    });

    const islands: TypeIsland[] = Array.from(byType.entries())
      .map(([type, beads]) => ({ type, beads }))
      .sort((a, b) => typeOrderRank(a.type) - typeOrderRank(b.type) || a.type.localeCompare(b.type));

    const counts = islands
      .map((island) => ({ type: island.type, count: island.beads.length }))
      .sort((a, b) => b.count - a.count || a.type.localeCompare(b.type));

    return { encounter, children, counts, islands };
  });

  // Roots: patient_registration and any bead with no parent edge and that is
  // not itself an encounter (encounters with no parent are still chapters,
  // handled above).
  const roots = graph.beads.filter((b) => b.type !== 'fhir_encounter' && !hasParentEdge.has(b.id));
  roots.sort((a, b) => a.timestamp.localeCompare(b.timestamp) || a.id.localeCompare(b.id));

  const positions: Record<string, { x: number; y: number }> = {};
  let currentY = 0;

  roots.forEach((root) => {
    positions[root.id] = { x: 0, y: currentY };
    currentY += encounterCardMinHeight + spineGapCollapsed;
  });

  chapters.forEach((chapter) => {
    const cardTopY = currentY;
    positions[chapter.encounter.id] = { x: 0, y: cardTopY };
    currentY += encounterCardMinHeight;

    if (expandedIds.has(chapter.encounter.id) && chapter.islands.length > 0) {
      // Islands lay out left-to-right, starting just right of the chapter
      // card, all vertically aligned to the card's top so the chapter card
      // reads as the "spine" the islands branch off from.
      let islandX = encounterCardWidth + islandGap;
      let maxIslandHeight = 0;

      chapter.islands.forEach((island) => {
        island.beads.forEach((bead, i) => {
          const row = Math.floor(i / tm.cols);
          const col = i % tm.cols;
          positions[bead.id] = {
            x: islandX + islandPadding + col * (tm.width + tm.gapX),
            y: cardTopY + islandHeaderHeight + islandPadding + row * (tm.height + tm.gapY),
          };
        });
        maxIslandHeight = Math.max(maxIslandHeight, islandHeight(island.beads.length, tm));
        islandX += islandWidth(island.beads.length, tm) + islandGap;
      });

      // Reserve room for the tallest island so the next chapter card cannot
      // overlap it, then advance past the card height already added.
      const islandsExtent = Math.max(0, maxIslandHeight - encounterCardMinHeight);
      currentY += islandsExtent + spineGapExpanded;
    } else {
      currentY += spineGapCollapsed;
    }
  });

  return { roots, chapters, positions, totalHeight: currentY };
}

// --- Figure-mode auto-focus (requirement 2) --------------------------------
//
// The whole point of the figure is a reader seeing BOTH axes at once: the
// vertical spine (encounters) and a horizontal arc (a clinical_link) landing
// on identifiable Beads at both ends. With the spine collapsed by default,
// every arc's endpoints resolve to encounter CARDS, which are already
// visible — but that shows only "these two encounters relate", not "these
// two BEADS relate", which is the actual claim clinical_links make. So in
// figure mode we auto-expand two encounters chosen so that:
//   1. they are adjacent in spine order (minimal vertical distance, so both
//      expanded islands can land in one viewport without an extreme zoom-out
//      that makes text illegible), and
//   2. they have the most clinical_links directly connecting their children
//      (so expanding them surfaces the richest cluster of visible arcs, not
//      an isolated pair).
// This is computed generically from `graph` (not hardcoded to any one
// patient's bead ids), so it holds for whichever patient is loaded.
//
// `linkedBeadIds` is every bead that is the actual endpoint of one of the
// clinical_links counted toward this pair (NOT every child of either
// encounter — an encounter can have 10-15 children and only a handful of
// them participate in the pair's links). This is what the caller should
// fit the viewport to: fitting to entire islands (verified via a Playwright
// capture) produces a huge bounding box that zooms out far enough to make
// tile text illegible again, defeating requirement 1; fitting to just the
// two encounter cards (also verified) crops out the very tiles the arcs
// connect, since islands extend far to the right of their card. Fitting to
// the two cards + only the linked tiles keeps the frame tight around
// exactly the arcs being highlighted while still showing the spine context.
interface FigureFocus {
  pair: [string, string];
  linkedBeadIds: string[];
}

function findFigureFocusPair(graph: PatientGraph): FigureFocus | null {
  const beadById = new Map(graph.beads.map((b) => [b.id, b]));
  const encounters = graph.beads.filter((b) => b.type === 'fhir_encounter');
  if (encounters.length < 2) return null;

  // Spine order matches layoutBeadGraph's chapter sort: newest-at-top.
  const spineOrder = encounters
    .slice()
    .sort((a, b) => b.timestamp.localeCompare(a.timestamp) || b.id.localeCompare(a.id));
  const rankOf = new Map(spineOrder.map((e, i) => [e.id, i]));

  const parentOf = new Map<string, string>();
  graph.edges.forEach((e) => parentOf.set(e.child_id, e.parent_id));
  const encounterIds = new Set(encounters.map((e) => e.id));

  function owningEncounter(beadId: string): string | null {
    const p = parentOf.get(beadId);
    return p && encounterIds.has(p) ? p : null;
  }

  // Count clinical_links per (encounter, encounter) pair whose two beads
  // belong to different encounters, tracking the minimum spine-rank distance
  // observed for that pair and the specific bead ids each link connects.
  const linkCountByPair = new Map<string, number>();
  const linkedBeadIdsByPair = new Map<string, Set<string>>();
  graph.links.forEach((link) => {
    if (!beadById.has(link.bead_a) || !beadById.has(link.bead_b)) return;
    const ea = owningEncounter(link.bead_a);
    const eb = owningEncounter(link.bead_b);
    if (!ea || !eb || ea === eb) return;
    const key = ea < eb ? `${ea}::${eb}` : `${eb}::${ea}`;
    linkCountByPair.set(key, (linkCountByPair.get(key) ?? 0) + 1);
    const beadSet = linkedBeadIdsByPair.get(key) ?? new Set<string>();
    beadSet.add(link.bead_a);
    beadSet.add(link.bead_b);
    linkedBeadIdsByPair.set(key, beadSet);
  });

  let best: { key: string; dist: number; count: number } | null = null;
  linkCountByPair.forEach((count, key) => {
    const [ea, eb] = key.split('::');
    const ra = rankOf.get(ea);
    const rb = rankOf.get(eb);
    if (ra === undefined || rb === undefined) return;
    const dist = Math.abs(ra - rb);
    // Prefer smallest spine distance first (fits one viewport), then most
    // links as the tiebreaker (richest visible cluster).
    if (!best || dist < best.dist || (dist === best.dist && count > best.count)) {
      best = { key, dist, count };
    }
  });

  if (!best) return null;
  const { key } = best as { key: string };
  const [ea, eb] = key.split('::');
  return {
    pair: [ea, eb],
    linkedBeadIds: Array.from(linkedBeadIdsByPair.get(key) ?? []),
  };
}

// --- Custom edge: arcs curve to one side of the spine, independent of the
// (mostly single-column) node X positions — a plain bezier between two nodes
// stacked in the same column would read as a nearly straight vertical line,
// which does not communicate "these two are linked". We compute an explicit
// quadratic path bowing to the right by an amount proportional to vertical
// distance (capped), so short/nearby arcs stay tight and long-distance arcs
// bow out further without ever overlapping the spine itself.
function arcPath(sourceX: number, sourceY: number, targetX: number, targetY: number): [string, number, number] {
  const dy = Math.abs(targetY - sourceY);
  const bow = Math.min(40 + dy * 0.18, 220);
  const baseX = Math.max(sourceX, targetX);
  const controlX = baseX + bow;
  const midY = (sourceY + targetY) / 2;
  const path = `M ${sourceX},${sourceY} Q ${controlX},${midY} ${targetX},${targetY}`;
  return [path, controlX * 0.55 + baseX * 0.45, midY];
}

function ArcLinkEdge({ id, sourceX, sourceY, targetX, targetY, data, markerEnd }: EdgeProps<ClinicalLinkEdgeData>) {
  const [edgePath, labelX, labelY] = arcPath(sourceX, sourceY, targetX, targetY);
  const figureMode = data?.figureMode === true;
  const quiet = data?.evidenceBasis === 'cooccurrence';

  const strokeStyle = figureMode
    ? FIGURE_SEVERITY_STYLE[data?.severity ?? 'info']
    : { ...SEVERITY_STYLE[data?.severity ?? 'info'], opacity: quiet ? 0.32 : 0.85 };

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        markerEnd={markerEnd}
        style={{
          stroke: strokeStyle.stroke,
          strokeWidth: strokeStyle.width,
          // Quiet (info/co-occurrence) links are the overwhelming majority
          // of clinical_links (a DB CHECK constraint enforces
          // severity='info' whenever evidence_basis='cooccurrence' — see
          // migrations_0006_test.go), which is exactly the "haze" a
          // paper-figure capture reported as visually noisy at 483 links.
          // Lowered further (0.5 -> 0.32) so the co-occurrence mass recedes
          // and true warning/alert/critical arcs stay legible by contrast —
          // static tuning only, no new interactive state (kept intentionally
          // simple per this round's "optional, don't over-engineer" note).
          // Figure mode (requirement 2) instead uses FIGURE_SEVERITY_STYLE,
          // which raises the whole scale (width AND opacity) so `info` reads
          // as a deliberate line in print rather than an artifact, while
          // keeping the info < warning < alert < critical ordering intact.
          opacity: strokeStyle.opacity,
          fill: 'none',
        }}
      />
      <EdgeLabelRenderer>
        {/* `group` + `group-hover` keeps the provenance card CSS-only. It must
            NOT be React state: this file documents an infinite flicker loop
            caused by feeding hover state back into the nodes/edges useMemo,
            which recreated the very DOM elements hover-detection depends on. */}
        <div
          className="nodrag nopan group"
          style={{
            position: 'absolute',
            transform: `translate(0, -50%) translate(${labelX}px,${labelY}px)`,
            pointerEvents: 'all',
          }}
        >
          <span
            className={figureMode ? 'text-[10px] font-semibold px-1.5 py-0.5 rounded bg-white border shadow-sm whitespace-nowrap' : 'text-[9px] font-medium px-1 py-0.5 rounded bg-white/90 border shadow-sm whitespace-nowrap'}
            style={{ borderColor: strokeStyle.stroke, color: strokeStyle.stroke, opacity: quiet ? 0.7 : 1 }}
          >
            {/* Figure mode (requirement 2 bonus): show the specific
                matched_tag (e.g. "rxnorm:308136", "atc:c09aa03") rather than
                the generic `relation` string ("clinical_correlation" for
                every link in the corpus) — the tag is the part that actually
                differentiates one arc from another and is what a reader of
                the paper figure needs to see "why" two Beads are linked.
                Falls back to `relation` if a link has no matched_tag. */}
            {figureMode ? data?.matchedTag || data?.relation : data?.relation}
          </span>

          {/* Provenance card. This is the on-screen form of the paper's audit
              claim: a rendered relationship names the projection run that wrote
              it, and that run's manifest names the immutable, content-addressed
              knowledge Bead (rule_version) that asserted it. Hidden until hover
              so the graph stays readable at rest. */}
          <div className="hidden group-hover:block absolute left-0 top-full mt-1 z-50 w-80 rounded-lg border border-slate-300 bg-white p-3 shadow-xl text-left">
            <div className="mb-2 flex items-center gap-2">
              <span
                className="px-1.5 py-0.5 rounded text-[10px] font-semibold uppercase tracking-wide"
                style={{ backgroundColor: strokeStyle.stroke, color: '#fff' }}
              >
                {data?.severity ?? 'info'}
              </span>
              <span className="text-xs font-semibold text-slate-800">{data?.relation}</span>
            </div>

            <dl className="space-y-1 text-[11px]">
              <div className="flex gap-2">
                <dt className="w-28 shrink-0 text-slate-500">matched tag</dt>
                <dd className="font-mono text-slate-800 break-all">{data?.matchedTag || '—'}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="w-28 shrink-0 text-slate-500">evidence basis</dt>
                <dd className="text-slate-800">{data?.evidenceBasis || '—'}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="w-28 shrink-0 text-slate-500">rule Bead</dt>
                <dd className="font-mono text-slate-800 break-all">
                  {data?.ruleVersion ? `${data.ruleVersion.slice(0, 16)}…` : '—'}
                </dd>
              </div>
              <div className="flex gap-2">
                <dt className="w-28 shrink-0 text-slate-500">projection run</dt>
                <dd className="font-mono text-slate-800 break-all">
                  {data?.projectionRunID ? `${data.projectionRunID.slice(0, 16)}…` : '—'}
                </dd>
              </div>
            </dl>

            <p className="mt-2 border-t border-slate-100 pt-2 text-[10px] leading-snug text-slate-500">
              Derived, not stored: this relationship is a projection. The run resolves through
              <span className="font-mono"> projection_manifest </span>
              to the immutable rule Bead that asserted it.
            </p>
          </div>
        </div>
      </EdgeLabelRenderer>
    </>
  );
}

const spineEdgeTypes = { arcLink: ArcLinkEdge };

function CountBadges({ counts }: { counts: Array<{ type: string; count: number }> }) {
  if (counts.length === 0) {
    return <span className="text-[10px] italic" style={{ color: '#9a9a94' }}>no linked records</span>;
  }
  const text = counts.map((c) => `${c.count} ${pluralizeType(c.type, c.count)}`).join(' · ');
  return <span className="text-[10px]" style={{ color: '#5b5b56' }}>{text}</span>;
}

// --- Bead tile: compact by default, full content on hover -----------------
//
// Tiles in an expanded island are deliberately tiny (fit 3-per-row), so the
// visible label is just the type icon — the full summary/type/timestamp/
// status only needs to be legible ON HOVER. A plain `title` attribute works
// but is slow to appear and unstyled; this renders an HTML popover instead,
// following the same on-hover-reveal pattern as ArcLinkEdge's
// EdgeLabelRenderer hover label elsewhere in this file.
//
// IMPORTANT — history of two bugs and why this is now CSS-only, not React
// state:
//
// Bug 1 (stacking context): ReactFlow renders every node as its own
// `transform`-positioned element; sibling node wrappers share one DOM
// parent, but a z-index set only inside THIS component's own popover div
// never wins against a *different* sibling node's wrapper. Verified by
// reading @reactflow/core dist/esm/index.js's NodeWrapper: the wrapper's
// real style is `{ zIndex: <computed>, transform, ..., ...style }` — the
// caller's `style` prop (i.e. a node's `style.zIndex`) is spread LAST, so it
// wins over ReactFlow's own computed zIndex and lands as the real DOM
// z-index. Every tile shared the same static baseline z-index, so ties broke
// by DOM/paint order, not by hover.
//
// Bug 2 (render loop from the first fix attempt): fixing bug 1 by tracking
// `hoveredNodeId` as React state in the parent (BeadGraphView) and feeding it
// into the `useMemo` that builds the `nodes` array caused a flicker/infinite
// loop, verified by reading the actual call graph: `hoveredNodeId` was a
// `useMemo` dependency (GraphView.tsx's node-building memo) → every hover
// change recomputed the ENTIRE nodes/edges array from scratch, producing
// brand-new `data.label` React elements for every node → React remounts the
// label DOM for the hovered node's new element identity → the physical DOM
// element under the cursor is replaced → the browser fires `mouseleave` on
// the old (now-detached) element and `mouseenter` on the new one, even
// though the pointer never moved → `onHoverChange` fires again → state
// updates again → back to the top. The hover state was recreating the very
// DOM elements hover-detection depends on.
//
// Fix: hover is now handled ENTIRELY in CSS (`:hover`), which triggers zero
// React re-renders and therefore cannot feed back into itself. The popover
// is always rendered in the DOM (not conditionally, per `hovered` state) and
// hidden via CSS by default; `.bead-tile-node:hover .bead-tile-popover`
// (see ui/src/index.css) reveals it. The node's z-index bump on hover is
// also pure CSS (`.bead-tile-node:hover { z-index: ... !important }`) — the
// `!important` is required because, per Bug 1's finding above, ReactFlow's
// NodeWrapper puts `node.style.zIndex` inline on the wrapper, and inline
// style otherwise always beats a CSS class rule; this component therefore
// no longer sets `style.zIndex` at all for tile/encounter/root nodes (CSS
// owns the baseline AND the hover bump), so there is nothing left for
// `:hover` to have to out-rank besides ReactFlow's own default (which is
// unset/0 unless `node.zIndex` is given, and this code never sets that
// either).
function BeadTile({
  bead,
  status,
  popoverAlign = 'center',
  figureMode = false,
}: {
  bead: GraphBead;
  status: NormalizedStatus;
  // Bias the popover horizontally so it does not run off the right edge of
  // the canvas for tiles in the last column of an island's grid (the
  // practical "near the edge" case for this left-to-right island layout) —
  // 'left' opens the popover leftward from the tile's right edge instead of
  // centering it, keeping it fully on-screen for those tiles. This is a
  // static, layout-time value (known from the tile's grid column), not
  // something that needs to be recomputed on hover.
  popoverAlign?: 'left' | 'center';
  // Requirement 1: in figure mode the tile itself must show a short text
  // label (type + summary) — a hover popover is useless in a static
  // screenshot. The popover below is still rendered (harmless, CSS-hidden by
  // default) so normal interactive use inside figure mode is unaffected; it
  // is simply redundant with the always-visible text in this mode.
  figureMode?: boolean;
}) {
  const icon = beadTypeLabel(bead.type).split(' ')[0]; // just the emoji glyph
  const shortType = shortBeadType(bead.type);

  return (
    <div className="nodrag nopan" style={{ position: 'relative', width: '100%', height: '100%' }}>
      {figureMode ? (
        <div
          style={{
            textAlign: 'left',
            lineHeight: 1.25,
            textDecoration: status === 'retracted' ? 'line-through' : 'none',
            opacity: status === 'retracted' ? 0.75 : 1,
          }}
        >
          <div style={{ fontSize: '11px', fontWeight: 700, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {icon} {shortType}
          </div>
          <div
            style={{
              fontSize: '10px',
              marginTop: 1,
              // Requirement 1: legibility beats density — allow the clinical
              // name to wrap onto a second line instead of clipping via
              // ellipsis, now that the redundant "<type>: " prefix
              // (displaySummary) is stripped and FIGURE_TILE_METRICS gives it
              // room. Capped at 2 lines (not unlimited) so an unusually long
              // summary cannot blow out the island grid layout, which is
              // computed from a fixed tile height in layoutBeadGraph.
              display: '-webkit-box',
              WebkitLineClamp: 2,
              WebkitBoxOrient: 'vertical' as const,
              overflow: 'hidden',
            }}
          >
            {displaySummary(bead) || '(no summary)'}
          </div>
        </div>
      ) : (
        <div
          style={{
            textAlign: 'center',
            lineHeight: 1.2,
            fontSize: '13px',
            textDecoration: status === 'retracted' ? 'line-through' : 'none',
            opacity: status === 'retracted' ? 0.7 : 1,
          }}
        >
          {icon}
        </div>
      )}
      {/* Always in the DOM; visibility + reveal is pure CSS (see
          .bead-tile-popover in ui/src/index.css) — no React state, no
          re-render on hover, no loop. */}
      <div
        className="bead-tile-popover"
        style={{
          position: 'absolute',
          top: '100%',
          left: popoverAlign === 'left' ? 'auto' : '50%',
          right: popoverAlign === 'left' ? 0 : 'auto',
          transform: popoverAlign === 'left' ? 'translate(0, 6px)' : 'translate(-50%, 6px)',
          pointerEvents: 'none',
        }}
      >
        <div
          className="text-left shadow-lg rounded-md border"
          style={{
            background: '#ffffff',
            borderColor: PAPER.spine,
            color: PAPER.ink,
            width: 220,
            padding: '8px 10px',
            fontSize: '11px',
            lineHeight: 1.4,
          }}
        >
          <div style={{ fontWeight: 600, marginBottom: 2 }}>{beadTypeLabel(bead.type)}</div>
          <div
            style={{
              textDecoration: status === 'retracted' ? 'line-through' : 'none',
              opacity: status === 'retracted' ? 0.75 : 1,
              marginBottom: 4,
            }}
          >
            {displaySummary(bead) || '(no summary)'}
          </div>
          <div style={{ fontSize: '10px', color: '#5b5b56' }}>
            {formatLocalDate(bead.timestamp)} · {status}
          </div>
        </div>
      </div>
    </div>
  );
}

function BeadGraphView({
  graph,
  onBeadClick,
  selectedBeadId,
  figureMode = false,
}: {
  graph: PatientGraph;
  onBeadClick?: (bead: GraphBead) => void;
  selectedBeadId?: string;
  figureMode?: boolean;
}) {
  const [expandedIds, setExpandedIds] = useState<Set<string>>(() => new Set());
  // Legend starts OPEN by default (top-right, clear of the spine which
  // originates top-left — see defaultViewport below) — still collapsible via
  // its header toggle for users who want the extra width back.
  const [legendOpen, setLegendOpen] = useState(true);
  // NOTE: hover is intentionally NOT React state. An earlier attempt tracked
  // `hoveredNodeId` here and fed it into the nodes/edges useMemo below to
  // drive z-index — that caused a render loop (see BeadTile's doc comment
  // for the full call graph). Hover-driven z-index and popover visibility
  // are now handled entirely in CSS (`.bead-tile-node:hover` etc. in
  // ui/src/index.css), which triggers no React re-render at all.
  //
  // `figureMode` is a plain prop (owned by the App-level toolbar toggle, see
  // GraphView's default export below), not local state derived from hover or
  // any per-render computation — it is a stable dependency for the nodes/
  // edges useMemo below, so it cannot reintroduce the Bug 2 render loop
  // documented on BeadTile: it only changes when the user explicitly clicks
  // the toolbar toggle, exactly like `expandedIds`.

  const toggleExpanded = useCallback((encounterId: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(encounterId)) next.delete(encounterId);
      else next.add(encounterId);
      return next;
    });
  }, []);

  const { fitView } = useReactFlow();

  // Requirement 2 — figure-mode auto-focus: in figure mode, a pair of nearby
  // encounters whose children are linked by clinical_links (findFigureFocus
  // Pair, computed generically from `graph`, not hardcoded to any patient)
  // is treated as always-expanded, so a static screenshot shows at least one
  // arc with both endpoints on identifiable Beads rather than every link
  // resolving to a collapsed encounter card (or sweeping off-screen to a
  // temporally distant chapter). This is derived at render time — a `Set`
  // union of the user's own manual `expandedIds` toggles with the auto-focus
  // pair — rather than an effect that calls setExpandedIds on mount
  // (react-hooks/set-state-in-effect correctly flags that pattern: it is
  // exactly the kind of "synchronize derived state via an extra render"
  // effect https://react.dev/learn/you-might-not-need-an-effect warns
  // against). It never shrinks the user's own selection, only adds to it,
  // and is a plain function of existing props/state, so it cannot
  // reintroduce the render loop documented on BeadTile (hover is still not
  // involved anywhere in this computation).
  const effectiveExpandedIds = useMemo(() => {
    if (!figureMode) return expandedIds;
    const focus = findFigureFocusPair(graph);
    if (!focus) return expandedIds;
    if (focus.pair.every((id) => expandedIds.has(id))) return expandedIds;
    const next = new Set(expandedIds);
    focus.pair.forEach((id) => next.add(id));
    return next;
  }, [figureMode, graph, expandedIds]);

  const { nodes, edges } = useMemo(() => {
    const tm = tileMetricsFor(figureMode);
    const layout = layoutBeadGraph(graph, effectiveExpandedIds, tm);
    const beadById = new Map(graph.beads.map((b) => [b.id, b]));

    // Resolve every bead id to the node id it should currently render as: the
    // bead itself if it is a root/encounter/expanded child, or its owning
    // encounter's id if it is collapsed inside a chapter. This is what makes
    // arcs land on the encounter card when collapsed and the specific child
    // bead when expanded (per design direction).
    const encounterOfChild = new Map<string, string>();
    layout.chapters.forEach((chapter) => {
      chapter.children.forEach((child) => {
        encounterOfChild.set(child.id, chapter.encounter.id);
      });
    });

    function renderNodeIdFor(beadId: string): string | null {
      if (layout.positions[beadId]) return beadId; // root, encounter, or expanded child
      const encId = encounterOfChild.get(beadId);
      if (encId && layout.positions[encId]) return encId; // collapsed -> owning chapter card
      return null;
    }

    const nodes: Node[] = [];

    layout.roots.forEach((root) => {
      const pos = layout.positions[root.id];
      const isSelected = selectedBeadId === root.id;
      nodes.push({
        id: root.id,
        position: pos,
        sourcePosition: Position.Bottom,
        targetPosition: Position.Top,
        className: 'bead-root-node',
        data: {
          label: (
            <div style={{ textAlign: 'left', lineHeight: 1.3 }}>
              <div style={{ fontWeight: 600, fontSize: '11px' }}>{beadTypeLabel(root.type)}</div>
              <div style={{ fontSize: '10px' }}>{truncate(displaySummary(root), 40)}</div>
              <div style={{ fontSize: '9px', opacity: 0.7 }}>{formatLocalDate(root.timestamp)}</div>
            </div>
          ),
        },
        style: {
          background: PAPER.spineTint,
          border: `2px solid ${PAPER.spine}`,
          color: PAPER.ink,
          borderRadius: '8px',
          fontSize: '11px',
          padding: '8px 10px',
          width: rootCardWidth,
          minHeight: encounterCardMinHeight,
          boxShadow: isSelected ? '0 0 0 3px rgba(59,49,112,0.35)' : '0 1px 2px rgba(26,32,39,0.12)',
          cursor: 'pointer',
          // NOTE: no inline zIndex here — baseline AND hover-bump z-index for
          // this node kind are owned entirely by CSS (`.bead-root-node` /
          // `.bead-root-node:hover` in ui/src/index.css). See BeadTile's doc
          // comment for why this must not be React state.
        },
      });
    });

    layout.chapters.forEach((chapter) => {
      const { encounter, children, counts, islands } = chapter;
      const pos = layout.positions[encounter.id];
      const isSelected = selectedBeadId === encounter.id;
      const isExpanded = effectiveExpandedIds.has(encounter.id);
      const status = normalizeStatus(encounter.status);
      const statusStyle = STATUS_STYLE[status];

      nodes.push({
        id: encounter.id,
        position: pos,
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
        className: 'bead-encounter-node',
        data: {
          label: (
            <div
              style={{ textAlign: 'left', lineHeight: 1.35, cursor: children.length > 0 ? 'pointer' : 'default' }}
              onClick={(e) => {
                if (children.length === 0) return;
                e.stopPropagation();
                toggleExpanded(encounter.id);
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
                <span
                  style={{
                    fontWeight: 600,
                    fontSize: '12px',
                    textDecoration: status === 'retracted' ? 'line-through' : 'none',
                  }}
                >
                  {formatLocalDate(encounter.timestamp)} · {beadTypeLabel(encounter.type)}
                </span>
                {children.length > 0 && (
                  <span style={{ fontSize: '10px', color: PAPER.spine, fontWeight: 600 }}>
                    {isExpanded ? '▾' : '▸'}
                  </span>
                )}
              </div>
              <div style={{ fontSize: '10px', opacity: 0.85 }}>{truncate(displaySummary(encounter), 44)}</div>
              <div style={{ marginTop: 2 }}>
                <CountBadges counts={counts} />
              </div>
            </div>
          ),
        },
        style: {
          background: statusStyle.bg,
          border: `${statusStyle.borderWidth} ${statusStyle.borderStyle} ${statusStyle.border}`,
          color: statusStyle.text,
          borderRadius: '10px',
          fontSize: '11px',
          padding: '8px 10px',
          width: encounterCardWidth,
          minHeight: encounterCardMinHeight,
          boxShadow: isSelected ? '0 0 0 3px rgba(59,49,112,0.35)' : '0 1px 3px rgba(26,32,39,0.12)',
          // NOTE: no inline zIndex — see .bead-encounter-node in
          // ui/src/index.css and BeadTile's doc comment above.
        },
      });

      if (isExpanded) {
        // Each type-island gets a non-interactive label node positioned just
        // above its tile grid ("observations · 12"), then its beads render
        // as small square-ish tiles wrapped into the grid computed by
        // layoutBeadGraph. This is the "meaning cluster, laid out
        // horizontally" the design calls for, instead of one tall vertical
        // list of up to 40 children.
        let islandX = encounterCardWidth + islandGap;
        islands.forEach((island) => {
          const headerY = pos.y;
          nodes.push({
            id: `island-header-${encounter.id}-${island.type}`,
            position: { x: islandX + islandPadding, y: headerY },
            draggable: false,
            selectable: false,
            connectable: false,
            data: {
              label: (
                <div style={{ fontSize: '9px', fontWeight: 600, color: PAPER.spine, whiteSpace: 'nowrap' }}>
                  {pluralizeType(island.type, island.beads.length)} · {island.beads.length}
                </div>
              ),
            },
            style: {
              background: 'transparent',
              border: 'none',
              padding: 0,
              width: islandWidth(island.beads.length, tm) - islandPadding * 2,
              minHeight: islandHeaderHeight,
              pointerEvents: 'none' as const,
              zIndex: 8,
            },
          });

          island.beads.forEach((child, childIndex) => {
            const childPos = layout.positions[child.id];
            const childStatus = normalizeStatus(child.status);
            const childStyle = STATUS_STYLE[childStatus];
            const isChildSelected = selectedBeadId === child.id;
            // Last column of the tile grid (per layoutBeadGraph's own
            // `col = i % tm.cols`) is the practical "near the right edge"
            // case for this left-to-right island layout — bias the popover
            // leftward there so it doesn't run off-screen. This is a static,
            // layout-time value; it does not depend on hover state.
            const isLastCol = childIndex % tm.cols === tm.cols - 1;
            nodes.push({
              id: child.id,
              position: childPos,
              sourcePosition: Position.Right,
              targetPosition: Position.Left,
              className: 'bead-tile-node',
              data: {
                label: (
                  <BeadTile
                    bead={child}
                    status={childStatus}
                    popoverAlign={isLastCol ? 'left' : 'center'}
                    figureMode={figureMode}
                  />
                ),
              },
              style: {
                background: childStyle.bg,
                border: `1.5px ${childStyle.borderStyle} ${childStyle.border}`,
                color: childStyle.text,
                borderRadius: '6px',
                padding: figureMode ? '5px 8px' : '3px 6px',
                width: tm.width,
                minHeight: tm.height,
                display: 'flex',
                alignItems: 'center',
                justifyContent: figureMode ? 'flex-start' : 'center',
                boxShadow: isChildSelected ? '0 0 0 2px rgba(59,49,112,0.35)' : 'none',
                cursor: 'pointer',
                // NOTE: no inline zIndex — baseline (9) AND hover-bump
                // z-index are owned by CSS (`.bead-tile-node` /
                // `.bead-tile-node:hover` in ui/src/index.css), not React
                // state. See BeadTile's doc comment for the two bugs this
                // avoids (stacking-context trap, then a render loop from the
                // first React-state-based fix attempt).
                overflow: 'visible',
              },
            });
          });

          islandX += islandWidth(island.beads.length, tm) + islandGap;
        });
      }
    });

    const edges: Edge[] = [];

    // Vertical spine: parent DAG edges are implicit in the stacked-card
    // layout itself (chapter -> its own expanded children are visually
    // grouped; no explicit line needed and none is drawn), so the only
    // rendered edges are the horizontal-axis clinical_links (arcs) and the
    // correction chains (amends/retracts), which can legitimately connect
    // across encounters (e.g. an amendment recorded at a later encounter).

    // Correction chains (amends/retracts): resolve each endpoint through the
    // same collapse-aware mapping as clinical_links so a chain into a
    // collapsed bead still renders (landing on the owning chapter card).
    graph.beads.forEach((bead) => {
      const beadNodeId = renderNodeIdFor(bead.id);
      if (!beadNodeId) return;
      bead.amends.forEach((targetId) => {
        if (!beadById.has(targetId)) return;
        const targetNodeId = renderNodeIdFor(targetId);
        if (!targetNodeId || targetNodeId === beadNodeId) return;
        edges.push({
          id: `amends-${bead.id}-${targetId}`,
          source: targetNodeId,
          target: beadNodeId,
          type: 'straight',
          label: 'amends',
          labelStyle: { fontSize: 9, fill: '#b7791f' },
          labelBgStyle: { fill: '#fffaf0' },
          style: { stroke: '#b7791f', strokeWidth: 2, strokeDasharray: '6 3' },
          markerEnd: { type: MarkerType.ArrowClosed, color: '#b7791f', width: 14, height: 14 },
        });
      });
      bead.retracts.forEach((targetId) => {
        if (!beadById.has(targetId)) return;
        const targetNodeId = renderNodeIdFor(targetId);
        if (!targetNodeId || targetNodeId === beadNodeId) return;
        edges.push({
          id: `retracts-${bead.id}-${targetId}`,
          source: targetNodeId,
          target: beadNodeId,
          type: 'straight',
          label: 'retracts',
          labelStyle: { fontSize: 9, fill: '#6b6b66' },
          labelBgStyle: { fill: '#f7f7f5' },
          style: { stroke: '#9a9a94', strokeWidth: 2, strokeDasharray: '3 3' },
          markerEnd: { type: MarkerType.ArrowClosed, color: '#9a9a94', width: 14, height: 14 },
        });
      });
    });

    // Horizontal axis: clinical_links, drawn as arcs. When a link's endpoint
    // bead is collapsed inside an encounter, the arc lands on that encounter
    // card; when expanded (or the endpoint is itself an encounter/root), it
    // lands on the specific bead.
    const seenLinkPairs = new Set<string>();
    graph.links.forEach((link) => {
      if (!beadById.has(link.bead_a) || !beadById.has(link.bead_b)) return;
      const sourceNodeId = renderNodeIdFor(link.bead_a);
      const targetNodeId = renderNodeIdFor(link.bead_b);
      if (!sourceNodeId || !targetNodeId || sourceNodeId === targetNodeId) return;
      // Figure mode draws only arcs whose BOTH endpoints land on a visible
      // Bead — i.e. both endpoint Beads live in an expanded encounter, so the
      // arc resolves to the Bead itself rather than being collapsed onto its
      // owning encounter card. Without this, every one of the patient's links
      // (483 for the reference patient, of which only a handful touch the
      // expanded encounters) is still drawn onto collapsed cards, burying the
      // arcs a reader is meant to trace in a hairball. The interactive default
      // keeps drawing all of them — collapsed arcs are useful for *browsing*
      // ("this encounter relates to that one"), but useless for a printed
      // figure, which must show which specific Beads a relationship joins.
      if (figureMode && (sourceNodeId !== link.bead_a || targetNodeId !== link.bead_b)) return;
      // Two links between the same pair of (collapsed) encounter cards would
      // draw duplicate overlapping arcs — dedupe by resolved node pair,
      // keeping the first (links are not pre-sorted by severity, but this
      // avoids visual clutter; hover still shows the specific relation of
      // whichever link happened to be kept).
      const pairKey = [sourceNodeId, targetNodeId].sort().join('::');
      if (seenLinkPairs.has(pairKey)) return;
      seenLinkPairs.add(pairKey);

      edges.push({
        id: `link-${link.link_id}`,
        source: sourceNodeId,
        target: targetNodeId,
        type: 'arcLink',
        data: {
          relation: link.relation,
          matchedTag: link.matched_tag,
          severity: link.severity,
          evidenceBasis: link.evidence_basis,
          ruleVersion: link.rule_version ?? '',
          projectionRunID: link.projection_run_id ?? '',
          figureMode,
        },
      });
    });

    return { nodes, edges };
  }, [graph, selectedBeadId, effectiveExpandedIds, toggleExpanded, figureMode]);

  const handleNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      const bead = graph.beads.find((b) => b.id === node.id);
      if (bead && onBeadClick) onBeadClick(bead);
    },
    [graph, onBeadClick],
  );

  // Requirement 2 (continued): once the auto-focus pair (see the effect
  // above) has actually been expanded into real nodes — i.e. `nodes` has
  // been rebuilt by the useMemo above and contains their child tiles — pan/
  // zoom so both focus encounter cards and the specific linked bead tiles
  // are visible together, which is what puts both endpoints of the arcs
  // between them inside the frame. Deliberately does NOT fit to the whole
  // 542-bead spine: that would zoom out far enough that arc labels/tile text
  // become illegible again, defeating requirement 1 (see the focusNodeIds
  // comment below for the two tighter alternatives that were also tried and
  // rejected). Only runs in figure mode — the interactive view keeps its
  // pinned top-left defaultViewport (see the ReactFlow props below)
  // untouched.
  useEffect(() => {
    if (!figureMode) return;
    const focus = findFigureFocusPair(graph);
    if (!focus) return;
    const { linkedBeadIds } = focus;
    // Fit to ONLY the specific bead tiles the clinical_links actually
    // connect (linkedBeadIds) — deliberately NOT the two encounter cards
    // themselves. Three things were tried and rejected first, all verified
    // with a Playwright capture + real DOM node positions read back from the
    // page:
    //   - fitting to just the two encounter cards: crops out the tiles
    //     entirely, since an expanded island renders well to the right of
    //     its card (layoutBeadGraph's `islandX = encounterCardWidth +
    //     islandGap`).
    //   - fitting to the two cards + EVERY child in both islands: bloats the
    //     bounding box enough that fitView zooms out to the point tile text
    //     is illegible (defeats requirement 1).
    //   - fitting to the two cards + only the linked tiles: still too wide,
    //     because the linked tiles' own island (e.g. medicationrequest) can
    //     sit ~2000px right of the card at x:0 simply because several OTHER
    //     islands (condition/observation/diagnosticreport/procedure) are
    //     laid out before it in TYPE_ORDER — the cards being pinned at x:0
    //     forces the same wide bounding box regardless of which children are
    //     selected. The task's actual requirement is "arcs land inside the
    //     frame with both endpoints on identifiable Beads", not "the
    //     originating encounter card must also be in frame" — the card is
    //     still one click away and its date/type is visible in the tile
    //     data itself, so dropping it from the FIT target (it still renders,
    //     just outside this frame) is what actually satisfies the
    //     requirement instead of fighting the existing island layout.
    const focusNodeIds = linkedBeadIds.map((id) => ({ id }));

    // Only attempt once the useMemo above has actually produced nodes for
    // the focus pair's linked tiles (guards the first render, where `nodes`
    // may still reflect the pre-expand state before effectiveExpandedIds
    // includes them).
    const ready = linkedBeadIds.every((id) => nodes.some((n) => n.id === id));
    if (!ready) return;

    // ReactFlow's fitView() silently no-ops (returns false, per its
    // `getNodes().every(n => n.width && n.height)` guard — see
    // @reactflow/core's fitView in store/utils) until the just-mounted
    // island/tile DOM nodes have been measured by its internal
    // ResizeObserver, which happens asynchronously after this effect's
    // nodes/edges update commits — a single requestAnimationFrame is too
    // early (verified: a Playwright capture showed the viewport transform
    // never leaving its defaultViewport value after only one rAF attempt).
    // Poll for a handful of frames instead of guessing a fixed delay, so
    // this fires on the very first frame where measurement is actually
    // done rather than either racing it or padding with an arbitrary
    // timeout.
    let attempts = 0;
    let rafId: number;
    const tryFit = () => {
      const didFit = fitView({ nodes: focusNodeIds, padding: 0.3, maxZoom: 0.9, duration: 0 });
      attempts += 1;
      if (!didFit && attempts < 30) {
        rafId = requestAnimationFrame(tryFit);
      }
    };
    rafId = requestAnimationFrame(tryFit);
    return () => cancelAnimationFrame(rafId);
  }, [figureMode, graph, nodes, fitView]);

  return (
    <ReactFlow
      className="bead-graph-spine"
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      edgeTypes={spineEdgeTypes}
      onNodeClick={handleNodeClick}
      // No declarative `fitView` prop: it computes a bounding-box fit over
      // the WHOLE graph (spine + any expanded islands to the right), so the
      // root/first chapter card can end up anywhere in the viewport
      // depending on aspect ratio — not reliably top-left. Instead pin the
      // initial viewport a small fixed margin from the content origin (roots
      // and encounter chapters are laid out starting at x:0,y:0 in
      // layoutBeadGraph), so patient_registration / the newest encounter is
      // always the first thing in view, at the top-left, at 1:1 zoom. Figure
      // mode overrides this afterward via the imperative `fitView()` effect
      // above, which re-pans/zooms to the two auto-focused encounters
      // specifically (not the whole graph) — this defaultViewport is simply
      // what is on screen for the one/two frames before that effect fires,
      // and is exactly what stays in effect for the (unaffected) interactive
      // view.
      defaultViewport={{ x: spineViewportMargin, y: spineViewportMargin, zoom: 1 }}
      minZoom={0.05}
      maxZoom={2}
      attributionPosition="bottom-right"
      proOptions={{ hideAttribution: true }}
      style={{ background: PAPER.bg }}
    >
      <Background color="#E4E1EC" gap={20} size={1} style={{ background: PAPER.bg }} />
      <Controls showInteractive={false} className="bg-white border border-slate-200 shadow-sm" />
      {/* MiniMap removed for this (paper-figure) view: for a 542-bead /
          483-link Clinical Spine it renders as a mostly-blank white
          rectangle in the bottom-right — visual noise with no information
          value at capture size. LegacyGraphView's MiniMap is untouched. */}

      {/* Legend: arc severity, correction dashes, status borders, collapse
          badges. Anchored top-RIGHT (not top-left, where the spine's
          root/encounter cards start at x:0/y:0 and would sit directly under
          it) and collapsible so it never covers Beads at capture size.
          Fully opaque (not translucent) + a strong border/shadow + a high
          z-index, so when an expanded island's tiles extend underneath it,
          the legend still reads unambiguously as "a panel in front of the
          graph" rather than blending with the tiles behind it (reported as
          a paper-figure noise issue at bead_graph capture size). */}
      <div
        className="absolute top-4 right-4 bg-white rounded-lg shadow-xl border-2 p-3 text-xs max-w-[240px]"
        style={{ borderColor: PAPER.spine, zIndex: 20000 }}
      >
        <button
          type="button"
          onClick={() => setLegendOpen((v) => !v)}
          className="flex items-center justify-between w-full font-semibold mb-1"
          style={{ color: PAPER.ink }}
        >
          <span>The Clinical Spine</span>
          <span className="text-[10px] font-normal" style={{ color: '#5b5b56' }}>{legendOpen ? '▾ hide' : '▸ legend'}</span>
        </button>

        {legendOpen && (
          <>
            <div className="text-[10px] mb-2" style={{ color: '#5b5b56' }}>
              Each card is an encounter. Click a card to expand its records; arcs show clinical relationships.
            </div>

            <div className="mb-2">
              <div className="font-medium mb-1" style={{ color: '#5b5b56' }}>Status (card border)</div>
              <div className="space-y-1">
                <LegendSwatch color={PAPER.activeAccent} label="active" />
                <LegendSwatch color="#b7791f" label="amended" doubleBorder />
                <LegendSwatch color="#9a9a94" label="retracted" strikethrough dashedBorder />
                <LegendSwatch color="#9a9a94" label="unattested" dashedBorder />
              </div>
            </div>

            <div className="mb-2">
              <div className="font-medium mb-1" style={{ color: '#5b5b56' }}>Clinical-link arc severity</div>
              <div className="space-y-1">
                {(() => {
                  // Legend reflects whichever severity scale is actually
                  // rendering right now (figure mode vs interactive), so it
                  // never claims a width the arcs don't actually use — see
                  // requirement 2's "legend stays truthful".
                  const scale = figureMode ? FIGURE_SEVERITY_STYLE : SEVERITY_STYLE;
                  return (
                    <>
                      <LegendLine color={scale.info.stroke} width={scale.info.width} label="info / co-occurrence" />
                      <LegendLine color={scale.warning.stroke} width={scale.warning.width} label="warning" />
                      <LegendLine color={scale.alert.stroke} width={scale.alert.width} label="alert" />
                      <LegendLine color={scale.critical.stroke} width={scale.critical.width} label="critical" />
                    </>
                  );
                })()}
              </div>
            </div>

            <div>
              <div className="font-medium mb-1" style={{ color: '#5b5b56' }}>Correction chains</div>
              <div className="space-y-1">
                <LegendLine color="#b7791f" width={2} dashed label="amends" />
                <LegendLine color="#9a9a94" width={2} dashed label="retracts" />
              </div>
            </div>
          </>
        )}
      </div>
    </ReactFlow>
  );
}

function LegendSwatch({
  color,
  label,
  strikethrough,
  dashedBorder,
  doubleBorder,
}: {
  color: string;
  label: string;
  strikethrough?: boolean;
  dashedBorder?: boolean;
  doubleBorder?: boolean;
}) {
  const borderStyle = doubleBorder ? 'double' : dashedBorder ? 'dashed' : 'solid';
  const borderWidth = doubleBorder ? '4px' : '2px';
  return (
    <div className="flex items-center gap-2">
      <div
        className="w-4 h-4 rounded"
        style={{
          background: `${color}22`,
          border: `${borderWidth} ${borderStyle} ${color}`,
        }}
      />
      <span className="text-slate-600" style={{ textDecoration: strikethrough ? 'line-through' : 'none' }}>
        {label}
      </span>
    </div>
  );
}

function LegendLine({ color, width, label, dashed }: { color: string; width: number; label: string; dashed?: boolean }) {
  return (
    <div className="flex items-center gap-2">
      <svg width="20" height="8" style={{ overflow: 'visible' }}>
        <line
          x1="0"
          y1="4"
          x2="20"
          y2="4"
          stroke={color}
          strokeWidth={width}
          strokeDasharray={dashed ? '4 2' : undefined}
        />
      </svg>
      <span className="text-slate-600">{label}</span>
    </div>
  );
}

export default function GraphView(props: GraphViewProps) {
  return (
    <div style={{ width: '100%', height: '100%' }} className="bg-slate-50">
      <ReactFlowProvider>
        {props.graph ? (
          <BeadGraphView
            graph={props.graph}
            onBeadClick={props.onBeadClick}
            selectedBeadId={props.selectedBeadId}
            figureMode={props.figureMode ?? false}
          />
        ) : (
          <LegacyGraphView
            items={props.items ?? []}
            onNodeClick={props.onNodeClick}
            selectedId={props.selectedId}
            restrictedIds={props.restrictedIds}
          />
        )}
      </ReactFlowProvider>
    </div>
  );
}
