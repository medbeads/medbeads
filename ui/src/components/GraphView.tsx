import { useMemo, useCallback } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  MarkerType,
  Position,
  ReactFlowProvider,
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  type Node,
  type Edge,
  type EdgeProps,
} from 'reactflow';
import 'reactflow/dist/style.css';
import {
  type TimelineItem,
  type ClearanceRule,
  type PatientGraph,
  type GraphBead,
  type GraphLinkSeverity,
  getViewerRoles,
} from '../lib/api';
import { isRestrictedForViewer } from '../lib/clearance';

interface GraphViewProps {
  // Legacy mode: date-grouped timeline items (parent edges only, client-side
  // clearance masking). Kept so any other caller of GraphView with the old
  // TimelineItem[] contract keeps working unchanged.
  items?: TimelineItem[];
  onNodeClick?: (item: TimelineItem) => void;
  selectedId?: string;
  clearanceRulesMap?: Record<string, ClearanceRule[]>;
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

// --- Severity / status visual vocabulary (R7, specs/R7_graph_view.md) -----
//
// severity: info = thin pale grey (co-occurrence always reads as quiet),
// warning = amber, alert/critical = red (critical thicker still).
const SEVERITY_STYLE: Record<GraphLinkSeverity, { stroke: string; width: number; dashed?: boolean }> = {
  info: { stroke: '#cbd5e1', width: 1 },
  warning: { stroke: '#f59e0b', width: 2 },
  alert: { stroke: '#ef4444', width: 2.5 },
  critical: { stroke: '#b91c1c', width: 3.5 },
};

// status: active=green accent / amended=amber accent / retracted=strikethrough
// + muted / unattested=dashed border. '' (absent bead_status row) === active,
// per graph_test.go's documented "absent = active" fallback.
type NormalizedStatus = 'active' | 'amended' | 'retracted' | 'unattested';

function normalizeStatus(status: GraphBead['status']): NormalizedStatus {
  if (status === 'amended' || status === 'retracted' || status === 'unattested') return status;
  return 'active';
}

const STATUS_STYLE: Record<NormalizedStatus, { bg: string; border: string; borderStyle: string; text: string }> = {
  active: { bg: '#f0fdf4', border: '#16a34a', borderStyle: 'solid', text: '#14532d' },
  amended: { bg: '#fffbeb', border: '#d97706', borderStyle: 'solid', text: '#78350f' },
  retracted: { bg: '#f8fafc', border: '#94a3b8', borderStyle: 'solid', text: '#64748b' },
  unattested: { bg: '#ffffff', border: '#94a3b8', borderStyle: 'dashed', text: '#334155' },
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

// --- Custom edge: clinical_links (horizontal axis) -------------------------
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

function ClinicalLinkEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  markerEnd,
}: EdgeProps<ClinicalLinkEdgeData>) {
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
    curvature: 0.35,
  });

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
          opacity: quiet ? 0.55 : 0.9,
        }}
      />
      <EdgeLabelRenderer>
        <div
          className="nodrag nopan"
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
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

const edgeTypes = { clinicalLink: ClinicalLinkEdge };

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

function LegacyGraphView({ items, onNodeClick, selectedId, clearanceRulesMap = {} }: Required<Pick<GraphViewProps, 'items'>> & Omit<GraphViewProps, 'items' | 'graph'>) {
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
      const rules = clearanceRulesMap[itemId];
      const isRestricted = isRestrictedForViewer(rules, viewerRoles);

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
  }, [items, selectedId, clearanceRulesMap, viewerRoles]);

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

// --- R7 bead-graph layout (PatientGraph mode) -------------------------------
//
// Y = time: beads are grouped by timestamp date, newest at top (same
// convention as the legacy layout). X = same-date beads laid out left to
// right, ordered so amend/retract chains and clinical_links stay visually
// close where possible (best-effort; ReactFlow's bezier links still connect
// correctly across rows regardless of X order).
function layoutBeadGraph(beads: GraphBead[]) {
  const byDate: Record<string, GraphBead[]> = {};
  beads.forEach((b) => {
    const dateKey = b.timestamp && b.timestamp.length >= 10 ? b.timestamp.substring(0, 10) : 'unknown';
    (byDate[dateKey] ??= []).push(b);
  });

  const sortedDates = Object.keys(byDate).sort((a, b) => {
    if (a === 'unknown') return 1;
    if (b === 'unknown') return -1;
    return b.localeCompare(a);
  });

  const positions: Record<string, { x: number; y: number }> = {};
  let currentY = 0;
  sortedDates.forEach((date) => {
    const rowBeads = byDate[date].slice().sort((a, b) => a.type.localeCompare(b.type) || a.id.localeCompare(b.id));
    rowBeads.forEach((b, index) => {
      positions[b.id] = { x: index * (nodeWidth + xGap), y: currentY };
    });
    currentY += nodeHeight + yGap;
  });

  return positions;
}

function BeadGraphView({ graph, onBeadClick, selectedBeadId }: { graph: PatientGraph; onBeadClick?: (bead: GraphBead) => void; selectedBeadId?: string }) {
  const { nodes, edges } = useMemo(() => {
    const positions = layoutBeadGraph(graph.beads);
    const beadById = new Map(graph.beads.map((b) => [b.id, b]));

    const nodes: Node[] = graph.beads.map((bead) => {
      const status = normalizeStatus(bead.status);
      const style = STATUS_STYLE[status];
      const isSelected = selectedBeadId === bead.id;
      const pos = positions[bead.id] ?? { x: 0, y: 0 };

      return {
        id: bead.id,
        position: pos,
        sourcePosition: Position.Bottom,
        targetPosition: Position.Top,
        data: {
          label: (
            <div style={{ textAlign: 'center', lineHeight: 1.35 }}>
              <div style={{ fontWeight: 600 }}>{beadTypeLabel(bead.type)}</div>
              <div
                style={{
                  textDecoration: status === 'retracted' ? 'line-through' : 'none',
                  opacity: status === 'retracted' ? 0.7 : 1,
                }}
              >
                {truncate(bead.summary)}
              </div>
              <div style={{ fontSize: '9px', opacity: 0.7 }}>{formatLocalDate(bead.timestamp)}</div>
            </div>
          ),
        },
        style: {
          background: style.bg,
          border: `2px ${style.borderStyle} ${style.border}`,
          color: style.text,
          borderRadius: '10px',
          fontSize: '11px',
          padding: '8px',
          width: nodeWidth,
          minHeight: nodeHeight,
          whiteSpace: 'pre-wrap' as const,
          boxShadow: isSelected ? '0 0 0 3px rgba(37, 99, 235, 0.35)' : '0 1px 3px rgba(0,0,0,0.1)',
          cursor: 'pointer',
          zIndex: 10,
        },
      };
    });

    const edges: Edge[] = [];

    // Vertical: parent DAG edges.
    graph.edges.forEach((e) => {
      if (!beadById.has(e.child_id) || !beadById.has(e.parent_id)) return;
      edges.push({
        id: `parent-${e.parent_id}-${e.child_id}`,
        source: e.parent_id,
        target: e.child_id,
        type: 'smoothstep',
        style: { stroke: '#94a3b8', strokeWidth: 1 },
        markerEnd: { type: MarkerType.ArrowClosed, color: '#94a3b8', width: 14, height: 14 },
      });
    });

    // Vertical: correction chains (amends/retracts), dashed + distinct color.
    graph.beads.forEach((bead) => {
      bead.amends.forEach((targetId) => {
        if (!beadById.has(targetId)) return;
        edges.push({
          id: `amends-${bead.id}-${targetId}`,
          source: targetId,
          target: bead.id,
          type: 'straight',
          label: 'amends',
          labelStyle: { fontSize: 9, fill: '#b45309' },
          labelBgStyle: { fill: '#fffbeb' },
          style: { stroke: '#d97706', strokeWidth: 2, strokeDasharray: '6 3' },
          markerEnd: { type: MarkerType.ArrowClosed, color: '#d97706', width: 14, height: 14 },
        });
      });
      bead.retracts.forEach((targetId) => {
        if (!beadById.has(targetId)) return;
        edges.push({
          id: `retracts-${bead.id}-${targetId}`,
          source: targetId,
          target: bead.id,
          type: 'straight',
          label: 'retracts',
          labelStyle: { fontSize: 9, fill: '#64748b' },
          labelBgStyle: { fill: '#f8fafc' },
          style: { stroke: '#94a3b8', strokeWidth: 2, strokeDasharray: '3 3' },
          markerEnd: { type: MarkerType.ArrowClosed, color: '#94a3b8', width: 14, height: 14 },
        });
      });
    });

    // Horizontal: clinical_links.
    graph.links.forEach((link) => {
      if (!beadById.has(link.bead_a) || !beadById.has(link.bead_b)) return;
      edges.push({
        id: `link-${link.link_id}`,
        source: link.bead_a,
        target: link.bead_b,
        type: 'clinicalLink',
        data: {
          relation: link.relation,
          matchedTag: link.matched_tag,
          severity: link.severity,
          evidenceBasis: link.evidence_basis,
        },
      });
    });

    return { nodes, edges };
  }, [graph, selectedBeadId]);

  const handleNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      const bead = graph.beads.find((b) => b.id === node.id);
      if (bead && onBeadClick) onBeadClick(bead);
    },
    [graph, onBeadClick],
  );

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      nodeTypes={nodeTypes}
      edgeTypes={edgeTypes}
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
        nodeColor={(n) => (n.style?.background as string) || '#fff'}
        nodeBorderRadius={2}
        className="border border-slate-200 shadow-lg rounded-lg overflow-hidden"
      />

      {/* Legend: severity colors, correction dashes, status encoding. */}
      <div className="absolute top-4 left-4 bg-white/90 backdrop-blur-sm rounded-lg shadow-md border border-slate-200 p-3 text-xs max-w-[220px]">
        <div className="font-semibold text-slate-700 mb-2">Bead Graph Legend</div>

        <div className="mb-2">
          <div className="text-slate-500 font-medium mb-1">Status</div>
          <div className="space-y-1">
            <LegendSwatch color="#16a34a" label="active" />
            <LegendSwatch color="#d97706" label="amended" />
            <LegendSwatch color="#94a3b8" label="retracted" strikethrough />
            <LegendSwatch color="#94a3b8" label="unattested" dashedBorder />
          </div>
        </div>

        <div className="mb-2">
          <div className="text-slate-500 font-medium mb-1">Link severity (horizontal)</div>
          <div className="space-y-1">
            <LegendLine color={SEVERITY_STYLE.info.stroke} width={1} label="info / co-occurrence" />
            <LegendLine color={SEVERITY_STYLE.warning.stroke} width={2} label="warning" />
            <LegendLine color={SEVERITY_STYLE.alert.stroke} width={2.5} label="alert" />
            <LegendLine color={SEVERITY_STYLE.critical.stroke} width={3.5} label="critical" />
          </div>
        </div>

        <div>
          <div className="text-slate-500 font-medium mb-1">Correction chains (vertical)</div>
          <div className="space-y-1">
            <LegendLine color="#d97706" width={2} dashed label="amends" />
            <LegendLine color="#94a3b8" width={2} dashed label="retracts" />
          </div>
        </div>
      </div>
    </ReactFlow>
  );
}

function LegendSwatch({ color, label, strikethrough, dashedBorder }: { color: string; label: string; strikethrough?: boolean; dashedBorder?: boolean }) {
  return (
    <div className="flex items-center gap-2">
      <div
        className="w-4 h-4 rounded"
        style={{
          background: `${color}22`,
          border: `2px ${dashedBorder ? 'dashed' : 'solid'} ${color}`,
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
            clearanceRulesMap={props.clearanceRulesMap}
          />
        )}
      </ReactFlowProvider>
    </div>
  );
}
