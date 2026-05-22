import { useMemo, useCallback } from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  MarkerType,
  Position,
  ReactFlowProvider,
  type Node,
  type Edge
} from 'reactflow';
import 'reactflow/dist/style.css';
import { type TimelineItem, type ClearanceRule, getViewerRoles } from '../lib/api';
import { isRestrictedForViewer } from '../lib/clearance';

interface GraphViewProps {
  items: TimelineItem[];
  onNodeClick?: (item: TimelineItem) => void;
  selectedId?: string;
  clearanceRulesMap?: Record<string, ClearanceRule[]>;
}

const nodeWidth = 180;
const nodeHeight = 70;
const xGap = 30;
const yGap = 120;

// Define nodeTypes and edgeTypes outside component to avoid React Flow warning
const nodeTypes = {};
const edgeTypes = {};

// Custom layout: Group by Date (Y axis = Time, X axis = Items in same date)
const getLayoutedElements = (nodes: Node[], edges: Edge[], items: TimelineItem[], restrictedNodeIds: Set<string>) => {
  // 1. Group nodes by date
  const nodesByDate: Record<string, Node[]> = {};

  nodes.forEach(node => {
    // Skip group nodes
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

  // 2. Sort dates (Newest first -> Top)
  const sortedDates = Object.keys(nodesByDate).sort((a, b) => {
      if (a === 'unknown') return 1;
      if (b === 'unknown') return -1;
      return b.localeCompare(a);
  });

  // 3. Assign positions - Start from 0,0, no negative coordinates
  let currentY = 0;

  sortedDates.forEach(date => {
    const rowNodes = nodesByDate[date];

    // Sort nodes within row:
    // 1. Access Status (Accessible first)
    // 2. Type
    rowNodes.sort((a, b) => {
        const isRestrictedA = restrictedNodeIds.has(a.id);
        const isRestrictedB = restrictedNodeIds.has(b.id);

        if (isRestrictedA !== isRestrictedB) {
            return isRestrictedA ? 1 : -1; // Accessible (false) comes before Restricted (true)
        }

        const typeA = items.find(i => i.data.id === a.id)?.type || '';
        const typeB = items.find(i => i.data.id === b.id)?.type || '';
        return typeA.localeCompare(typeB);
    });

    // Position nodes starting from x=0
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

function GraphViewInner({ items, onNodeClick, selectedId, clearanceRulesMap = {} }: GraphViewProps) {
  const viewerRoles = getViewerRoles();

  const { nodes: initialNodes, edges: initialEdges, clearanceGroups } = useMemo(() => {
    const newNodes: Node[] = [];
    const newEdges: Edge[] = [];
    const idSet = new Set(items.map(item => item.data.id));
    const restrictedNodeIds = new Set<string>();

    // Track accessible nodes by date for grouping
    const accessibleNodesByDate: Record<string, string[]> = {};

    items.forEach((item) => {
      const itemId = item.data.id;
      const isSelected = selectedId === itemId;
      const rules = clearanceRulesMap[itemId];
      const isRestricted = isRestrictedForViewer(rules, viewerRoles);

      if (isRestricted) {
          restrictedNodeIds.add(itemId);
      } else {
          // Track for grouping
          const dateKey = item.date && item.date.length >= 10 ? item.date.substring(0, 10) : 'unknown';
          if (!accessibleNodesByDate[dateKey]) {
              accessibleNodesByDate[dateKey] = [];
          }
          accessibleNodesByDate[dateKey].push(itemId);
      }

      let label = item.type.toUpperCase();
      let bgColor = "rgba(255, 255, 255, 0.95)";
      let borderColor = "#cbd5e1";

      // Base Styles
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

      // Restricted Overlay Styles
      if (isRestricted) {
          label = `🔒 ${label}`;
          bgColor = '#f1f5f9'; // Slate 100
          borderColor = '#cbd5e1'; // Slate 300
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

      // Edges (Parent -> Child)
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

    // Apply layout first to get positions
    const { nodes: layoutedNodes, edges: layoutedEdges } = getLayoutedElements(newNodes, newEdges, items, restrictedNodeIds);

    // Create group rectangles for Accessible Areas
    const groupNodes: Node[] = [];
    
    // We iterate through dates to create row-based accessible areas
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

      // Visual style for Accessible Area
      const color = { bg: 'rgba(34, 197, 94, 0.05)', border: 'rgba(34, 197, 94, 0.3)' }; // Greenish

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
          color: 'rgba(21, 128, 61, 0.8)', // Green 700
          padding: '4px 8px',
          display: 'flex',
          alignItems: 'flex-start',
          justifyContent: 'flex-start',
        },
        selectable: false,
        draggable: false,
      });
    });

    // Add group nodes at the beginning (so they render behind)
    const allNodes = [...groupNodes, ...layoutedNodes];

    return {
      nodes: allNodes,
      edges: layoutedEdges,
      clearanceGroups: Object.keys(accessibleNodesByDate)
    };
  }, [items, selectedId, clearanceRulesMap, viewerRoles]);

  const handleNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    // Ignore clicks on group nodes
    if (node.id.startsWith('clearance-group-')) return;

    const item = items.find(i => i.data.id === node.id);
    if (item && onNodeClick) onNodeClick(item);
  }, [items, onNodeClick]);

  return (
    <ReactFlow
      nodes={initialNodes}
      edges={initialEdges}
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

      {/* Legend for clearance colors */}
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

export default function GraphView(props: GraphViewProps) {
  return (
    <div style={{ width: '100%', height: '100%' }} className="bg-slate-50">
      <ReactFlowProvider>
        <GraphViewInner {...props} />
      </ReactFlowProvider>
    </div>
  );
}
