# CI: launch KyDrive (an exe, or shell:AppsFolder\<family>!App), then check that it started the
# core on $Socket and that its window shows the link form, which it renders only after the
# core has answered Status. Saves a screenshot to $Shot.
param([string]$Target, [string]$Socket, [string]$Shot)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Windows.Forms, System.Drawing
$ae = [Windows.Automation.AutomationElement]
function Find($scope, $name) {
    $scope.FindFirst([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.PropertyCondition]::new($ae::NameProperty, $name))
}

Start-Process $Target
$form = $null
for ($i = 0; $i -lt 60 -and -not $form; $i++) {
    Start-Sleep -Milliseconds 500
    $window = $ae::RootElement.FindFirst([Windows.Automation.TreeScope]::Children, [Windows.Automation.PropertyCondition]::new($ae::NameProperty, 'KyDrive'))
    if ($window) { $form = Find $window 'KyDrive address' }
}

$b = [Windows.Forms.Screen]::PrimaryScreen.Bounds
$bmp = [Drawing.Bitmap]::new($b.Width, $b.Height)
[Drawing.Graphics]::FromImage($bmp).CopyFromScreen($b.Location, [Drawing.Point]::Empty, $b.Size)
$bmp.Save($Shot)
Get-Process KyDrive, kydrive-sync -ErrorAction SilentlyContinue | Format-Table Id, Name, Path -AutoSize

if (-not (Test-Path $Socket)) { throw "no core socket at $Socket" }
if (-not $form) { throw 'the window never showed the link form' }
"link form shown; core socket at $Socket"
