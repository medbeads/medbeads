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
});
