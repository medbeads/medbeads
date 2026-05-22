import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ViewerRoleSelector } from '../components/ViewerRoleSelector';

describe('ViewerRoleSelector', () => {
  it('renders all roles and marks the selected one', () => {
    render(<ViewerRoleSelector selectedRoles={['primary_care']} onRolesChange={() => {}} />);
    expect(screen.getByText('Primary Care')).toBeInTheDocument();
    expect(screen.getByText('Insurance')).toBeInTheDocument();
    const primaryRadio = screen.getByRole('radio', { name: /primary care/i });
    expect(primaryRadio).toBeChecked();
  });

  it('calls onRolesChange with the chosen role', async () => {
    const onRolesChange = vi.fn();
    render(<ViewerRoleSelector selectedRoles={['primary_care']} onRolesChange={onRolesChange} />);

    await userEvent.click(screen.getByRole('radio', { name: /insurance/i }));

    expect(onRolesChange).toHaveBeenCalledWith(['insurance']);
  });

  it('adds a department role alongside the functional role', async () => {
    const onRolesChange = vi.fn();
    render(<ViewerRoleSelector selectedRoles={['specialist']} onRolesChange={onRolesChange} />);

    await userEvent.selectOptions(screen.getByRole('combobox'), 'dept:psychiatry');

    expect(onRolesChange).toHaveBeenCalledWith(['specialist', 'dept:psychiatry']);
  });

  it('shows the currently selected department', () => {
    render(
      <ViewerRoleSelector
        selectedRoles={['specialist', 'dept:genetics']}
        onRolesChange={() => {}}
      />,
    );
    expect(screen.getByRole('combobox')).toHaveValue('dept:genetics');
  });
});
