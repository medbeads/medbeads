import { useMemo, useCallback, useState } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  MarkerType,
  Position,
  ReactFlowProvider,
  BaseEdge,
  EdgeLabelRenderer,
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
const tileWidth = 96;
const tileHeight = 30;
const tileGapX = 6;
const tileGapY = 6;
const tileCols = 3; // wrap each island's tiles into a grid this many columns wide
const islandPadding = 8;
// Fixed pixel margin the initial viewport pans in from the content origin
// (0,0), where the root/first chapter card sits — keeps it comfortably clear
// of the pane edge and Controls, without ever letting fitView re-center the
// graph and hide the start of the spine off-screen.
const spineViewportMargin = 40;

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
function islandHeight(beadCount: number): number {
  const rows = Math.max(1, Math.ceil(beadCount / tileCols));
  return islandHeaderHeight + rows * tileHeight + Math.max(0, rows - 1) * tileGapY + islandPadding * 2;
}

function islandWidth(beadCount: number): number {
  const cols = Math.min(tileCols, Math.max(1, beadCount));
  return cols * tileWidth + Math.max(0, cols - 1) * tileGapX + islandPadding * 2;
}

function layoutBeadGraph(graph: PatientGraph, expandedIds: Set<string>): SpineLayout {
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
          const row = Math.floor(i / tileCols);
          const col = i % tileCols;
          positions[bead.id] = {
            x: islandX + islandPadding + col * (tileWidth + tileGapX),
            y: cardTopY + islandHeaderHeight + islandPadding + row * (tileHeight + tileGapY),
          };
        });
        maxIslandHeight = Math.max(maxIslandHeight, islandHeight(island.beads.length));
        islandX += islandWidth(island.beads.length) + islandGap;
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
  const style = SEVERITY_STYLE[data?.severity ?? 'info'];
  const quiet = data?.evidenceBasis === 'cooccurrence';

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        markerEnd={markerEnd}
        style={{
          stroke: style.stroke,
          strokeWidth: style.width,
          // Quiet (info/co-occurrence) links are the overwhelming majority
          // of clinical_links (a DB CHECK constraint enforces
          // severity='info' whenever evidence_basis='cooccurrence' — see
          // migrations_0006_test.go), which is exactly the "haze" a
          // paper-figure capture reported as visually noisy at 483 links.
          // Lowered further (0.5 -> 0.32) so the co-occurrence mass recedes
          // and true warning/alert/critical arcs stay legible by contrast —
          // static tuning only, no new interactive state (kept intentionally
          // simple per this round's "optional, don't over-engineer" note).
          opacity: quiet ? 0.32 : 0.85,
          fill: 'none',
        }}
      />
      <EdgeLabelRenderer>
        <div
          className="nodrag nopan"
          style={{
            position: 'absolute',
            transform: `translate(0, -50%) translate(${labelX}px,${labelY}px)`,
            pointerEvents: 'all',
          }}
          title={`${data?.relation ?? ''} (${data?.evidenceBasis ?? ''}, ${data?.severity ?? ''})${
            data?.matchedTag ? ` — ${data.matchedTag}` : ''
          }`}
        >
          <span
            className="text-[9px] font-medium px-1 py-0.5 rounded bg-white/90 border shadow-sm whitespace-nowrap"
            style={{ borderColor: style.stroke, color: style.stroke, opacity: quiet ? 0.7 : 1 }}
          >
            {data?.relation}
          </span>
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
}) {
  const icon = beadTypeLabel(bead.type).split(' ')[0]; // just the emoji glyph

  return (
    <div className="nodrag nopan" style={{ position: 'relative', width: '100%', height: '100%' }}>
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
            {bead.summary || '(no summary)'}
          </div>
          <div style={{ fontSize: '10px', color: '#5b5b56' }}>
            {formatLocalDate(bead.timestamp)} · {status}
          </div>
        </div>
      </div>
    </div>
  );
}

function BeadGraphView({ graph, onBeadClick, selectedBeadId }: { graph: PatientGraph; onBeadClick?: (bead: GraphBead) => void; selectedBeadId?: string }) {
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

  const toggleExpanded = useCallback((encounterId: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev);
      if (next.has(encounterId)) next.delete(encounterId);
      else next.add(encounterId);
      return next;
    });
  }, []);

  const { nodes, edges } = useMemo(() => {
    const layout = layoutBeadGraph(graph, expandedIds);
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
              <div style={{ fontSize: '10px' }}>{truncate(root.summary, 40)}</div>
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
      const isExpanded = expandedIds.has(encounter.id);
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
              <div style={{ fontSize: '10px', opacity: 0.85 }}>{truncate(encounter.summary, 44)}</div>
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
              width: islandWidth(island.beads.length) - islandPadding * 2,
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
            // `col = i % tileCols`) is the practical "near the right edge"
            // case for this left-to-right island layout — bias the popover
            // leftward there so it doesn't run off-screen. This is a static,
            // layout-time value; it does not depend on hover state.
            const isLastCol = childIndex % tileCols === tileCols - 1;
            nodes.push({
              id: child.id,
              position: childPos,
              sourcePosition: Position.Right,
              targetPosition: Position.Left,
              className: 'bead-tile-node',
              data: {
                label: <BeadTile bead={child} status={childStatus} popoverAlign={isLastCol ? 'left' : 'center'} />,
              },
              style: {
                background: childStyle.bg,
                border: `1.5px ${childStyle.borderStyle} ${childStyle.border}`,
                color: childStyle.text,
                borderRadius: '6px',
                padding: '3px 6px',
                width: tileWidth,
                minHeight: tileHeight,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
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

          islandX += islandWidth(island.beads.length) + islandGap;
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
        },
      });
    });

    return { nodes, edges };
  }, [graph, selectedBeadId, expandedIds, toggleExpanded]);

  const handleNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      const bead = graph.beads.find((b) => b.id === node.id);
      if (bead && onBeadClick) onBeadClick(bead);
    },
    [graph, onBeadClick],
  );

  return (
    <ReactFlow
      className="bead-graph-spine"
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      edgeTypes={spineEdgeTypes}
      onNodeClick={handleNodeClick}
      // No fitView: fitView computes a bounding-box fit over the WHOLE graph
      // (spine + any expanded islands to the right), so the root/first
      // chapter card can end up anywhere in the viewport depending on
      // aspect ratio — not reliably top-left. Instead pin the initial
      // viewport a small fixed margin from the content origin (roots and
      // encounter chapters are laid out starting at x:0,y:0 in
      // layoutBeadGraph), so patient_registration / the newest encounter is
      // always the first thing in view, at the top-left, at 1:1 zoom.
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
                <LegendLine color={SEVERITY_STYLE.info.stroke} width={1} label="info / co-occurrence" />
                <LegendLine color={SEVERITY_STYLE.warning.stroke} width={2.25} label="warning" />
                <LegendLine color={SEVERITY_STYLE.alert.stroke} width={3} label="alert" />
                <LegendLine color={SEVERITY_STYLE.critical.stroke} width={4} label="critical" />
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
          <BeadGraphView graph={props.graph} onBeadClick={props.onBeadClick} selectedBeadId={props.selectedBeadId} />
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
